package redisservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"loggingwayrpc/xivauth"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type RedisSessionService struct {
	client         *redis.Client
	xivauthservice *xivauth.XivAuthService
	prefix         string
	ttl            time.Duration
}

type UserSession struct {
	UserID      string
	Characters  []xivauth.Character
	AccessToken oauth2.Token
}

func NewRedisSessionService(addr string, password string, db int, prefix string, ttl time.Duration, xivauthserv *xivauth.XivAuthService) *RedisSessionService {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	err := rdb.Ping(context.Background()).Err()
	if err != nil {
		log.Fatalf("could not connect to Redis,SessionClient:%v", err)
	}
	return &RedisSessionService{
		client: rdb,
		prefix: prefix,
		ttl:    ttl,
	}
}

func (s *RedisSessionService) key(sessionID string) string {
	return fmt.Sprintf("%s:%s", s.prefix, sessionID)
}

func (s *RedisSessionService) CreateSession(ctx context.Context, sessionID string, session UserSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.key(sessionID), data, s.ttl).Err()
}

func (s *RedisSessionService) GetSession(ctx context.Context, sessionID string) (*UserSession, error) {
	val, err := s.client.Get(ctx, s.key(sessionID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errors.New("session not found")
		}
		return nil, err
	}

	var session UserSession
	if err := json.Unmarshal([]byte(val), &session); err != nil {
		return nil, err
	}

	return &session, nil
}

func (s *RedisSessionService) UpdateSession(ctx context.Context, sessionID string, session UserSession) error {
	return s.CreateSession(ctx, s.key(sessionID), session)
}

func (s *RedisSessionService) DeleteSession(ctx context.Context, sessionID string) error {
	return s.client.Del(ctx, s.key(sessionID)).Err()
}

func (s *RedisSessionService) Close() error {
	return s.client.Close()
}

var redirectMethod = "/loggingway_rpc.Loggingway/GetXivAuthRedirect"
var loginMethod = "/loggingway_rpc.Loggingway/Login"
var ignoreMethod = []string{redirectMethod, loginMethod}

func (s *RedisSessionService) AuthFunc(ctx context.Context) (context.Context, error) {
	method, _ := grpc.Method(ctx)
	for _, imethod := range ignoreMethod {
		if method == imethod {
			return ctx, nil
		}
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "metadata is not provided")
	}
	// extract token from authorization header
	token := md["authorization"]
	if len(token) == 0 {
		return nil, status.Error(codes.Unauthenticated, "authorization token is not provided")
	}

	// validate token and retrieve the userID
	sessionData, err := s.GetSession(ctx, token[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	return context.WithValue(ctx, "sessionData", sessionData), nil
}
