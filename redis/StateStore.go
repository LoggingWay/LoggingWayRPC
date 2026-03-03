package redisservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStateStoreService struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

type OAuthState struct {
	CreatedAt time.Time
	// Optional hardening
	UserAgent string
	IP        string
}

func NewRedisStateStoreService(addr string, password string, db int, prefix string, ttl time.Duration) *RedisStateStoreService {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	err := rdb.Ping(context.Background()).Err()
	if err != nil {
		log.Fatalf("could not connect to Redis,SessionClient:%v", err)
	}
	return &RedisStateStoreService{
		client: rdb,
		prefix: prefix,
		ttl:    ttl,
	}
}

func (s *RedisStateStoreService) key(state string) string {
	return fmt.Sprintf("%s:%s", s.prefix, state)
}

func (s *RedisStateStoreService) CreateNewState(ctx context.Context, state string, authst OAuthState) error {
	data, err := json.Marshal(authst)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.key(state), data, s.ttl).Err()
}

func (s *RedisStateStoreService) GetState(ctx context.Context, state string) (*OAuthState, error) {
	val, err := s.client.Get(ctx, s.key(state)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errors.New("state not found")
		}
		return nil, err
	}
	var oauthst OAuthState
	if err := json.Unmarshal([]byte(val), &oauthst); err != nil {
		return nil, err
	}
	return &oauthst, nil
}

func (s *RedisStateStoreService) DeleteState(ctx context.Context, state string) error {
	return s.client.Del(ctx, s.key(state)).Err()
}

func (s *RedisStateStoreService) Close() error {
	return s.client.Close()
}
