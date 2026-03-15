package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	lg "loggingwayrpc/gen/loggingway_rpc"
	redisservice "loggingwayrpc/redis"
	"loggingwayrpc/xivauth"
	"net"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noevain/xivauthgo"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	_ "github.com/joho/godotenv/autoload"
)

type loggingWayServer struct {
	lg.UnimplementedLoggingwayServer
	oauthConfig    *oauth2.Config
	connpool       *pgxpool.Pool
	xivAuthService *xivauth.XivAuthService
	Publisher      *redisservice.Publisher
	Sessioner      *redisservice.RedisSessionService
	Stater         *redisservice.RedisStateStoreService
}

type Report struct {
	ID         int64
	CreatedBy  uuid.UUID // pointer because it can be NULL
	ReportName string
	CreatedAt  time.Time
}

type EncounterPlayerStats struct {
	ID              int64
	EncounterID     int64
	PlayerID        int64
	PlayerName      string
	TotalDamage     int64
	TotalHealing    int64
	TotalHits       int64
	TotalCrits      int64
	TotalDirectHits int64
	FirstTimestamp  int64
	LastTimestamp   int64
	DurationSeconds float64
	DPS             float64
	HPS             float64
	CritRate        float64
	DirectHitRate   float64
	CreatedAt       time.Time
}

/*
TODO: either fully remove pgx, or create a service for it idk, I'm not thrilled about this server having db access,but for now I
have to
*/

func pgxconfidk() *pgxpool.Config {
	const defaultMaxConns = int32(4)
	const defaultMinConns = int32(0)
	const defaultMaxConnLifetime = time.Hour
	const defaultMaxConnIdleTime = time.Minute * 30
	const defaultHealthCheckPeriod = time.Minute
	const defaultConnectTimeout = time.Second * 5
	DATABASE_URL := os.Getenv("DB_STRING") //postgres:// user:pass@host:port/databasename

	dbConfig, err := pgxpool.ParseConfig(DATABASE_URL)
	if err != nil {
		log.Fatal("Failed to create a config, error: ", err)
	}

	dbConfig.MaxConns = defaultMaxConns
	dbConfig.MinConns = defaultMinConns
	dbConfig.MaxConnLifetime = defaultMaxConnLifetime
	dbConfig.MaxConnIdleTime = defaultMaxConnIdleTime
	dbConfig.HealthCheckPeriod = defaultHealthCheckPeriod
	dbConfig.ConnConfig.ConnectTimeout = defaultConnectTimeout
	return dbConfig
}
func newServer(redisesion *redisservice.RedisSessionService) *loggingWayServer {
	fmt.Println("Initializing xivauth service...")
	xivs, err := xivauth.NewService(os.Getenv("XIVAUTH_CLIENT_ID"), os.Getenv("XIVAUTH_CLIENT_SECRET"),
		os.Getenv("XIVAUTH_LOGIN_CALLBACK"),
		[]string{
			xivauth.ScopeUser,
			xivauth.ScopeCharacterAll,
		})
	if err != nil {
		log.Fatalf("Failed to initialize xivauthservice: %v", err)
	}
	fmt.Println("Initializing redis related services...")
	rediserv := redisservice.NewPublisher(os.Getenv("REDIS_ADDR"), "reports_stream")
	redistate := redisservice.NewRedisStateStoreService(os.Getenv("REDIS_ADDR"),
		"",
		0,
		"state",
		1*time.Hour)
	connPool, err := pgxpool.NewWithConfig(context.Background(), pgxconfidk())
	if err != nil {
		log.Fatalf("Failed to create database conn pool:%v", err)
	}
	s := &loggingWayServer{
		oauthConfig: xivauthgo.OAuth2Config(
			os.Getenv("XIVAUTH_CLIENT_ID"),
			os.Getenv("XIVAUTH_CLIENT_SECRET"),
			os.Getenv("XIVAUTH_LOGIN_CALLBACK"),
			[]string{
				xivauthgo.ScopeUser,
				xivauthgo.ScopeCharacterAll,
			},
		),
		xivAuthService: xivs,
		Publisher:      rediserv,
		Sessioner:      redisesion,
		Stater:         redistate,
		connpool:       connPool}
	fmt.Println("gRPC server init completed")
	return s
}

func (s *loggingWayServer) GetXivAuthRedirect(ctx context.Context, request *lg.GetXivAuthRedirectRequest) (*lg.GetXivAuthRedirectReply, error) {
	//fmt.Println(grpc.Method(ctx))
	state, err := generateState()
	if err != nil {
		st := status.New(codes.Internal, "State generation failed")
		return nil, st.Err()
	}
	stateinfo := redisservice.OAuthState{
		CreatedAt: time.Now(),
		UserAgent: "grpc",
		IP:        "something",
	}
	s.Stater.CreateNewState(ctx, state, stateinfo)
	return (&lg.GetXivAuthRedirectReply{Xivauthuri: xivauthgo.AuthCodeURL(s.oauthConfig, state)}), nil
}

func (s *loggingWayServer) Login(ctx context.Context, request *lg.LoginRequest) (*lg.LoginReply, error) {
	//check state
	_, err := s.Stater.GetState(ctx, request.State)
	if err != nil {
		return nil, status.Error(codes.DeadlineExceeded, "State expired or mismatch")
	}
	//maybe later implement user-agent/IP match idk
	token, err := s.xivAuthService.GetToken(request.Code)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Incorrect code")
	}
	user, err := s.xivAuthService.GetUser(token.AccessToken)
	if err != nil {
		return nil, status.Error(codes.Unknown, "Token obtained but failed to get user info")
	}
	character, err := s.xivAuthService.GetCharacters(token.AccessToken)
	if err != nil {
		return nil, status.Error(codes.Unknown, "Could not retrieve characters")
	}
	sessionData := redisservice.UserSession{
		UserID:      user.ID,
		AccessToken: *token,
		Characters:  character,
	}
	sessionID := uuid.NewString()
	err = s.Sessioner.CreateSession(ctx, sessionID, sessionData)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to create session")
	}
	return &lg.LoginReply{SessionID: sessionID}, nil
}

func (s *loggingWayServer) EncounterIngest(ctx context.Context, request *lg.NewEncounterRequest) (*lg.NewEncounterReply, error) {
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to acquire db connection")
	}
	query := `
		SELECT id, created_by, report_name, created_at
		FROM reports
		WHERE id = $1;
	`
	var r Report
	err = conn.QueryRow(ctx, query, request.ReportId).Scan(
		&r.ID,
		&r.CreatedBy,
		&r.ReportName,
		&r.CreatedAt,
	)
	if err != nil {
		return nil, status.Error(codes.Canceled, "report not found")
	}
	sessiondata := ctx.Value("sessionData")
	userid := uuid.MustParse(sessiondata.(*redisservice.UserSession).UserID)
	if r.CreatedBy != userid {
		return nil, status.Error(codes.PermissionDenied, "User does not own this report")
	}
	if err := s.Publisher.PublishEncounter(ctx, request.ReportId, request); err != nil {
		return nil, status.Error(codes.Canceled, "Error submiting report")
	}
	conn.Release()
	return &lg.NewEncounterReply{Code: 0}, nil
}

func (s *loggingWayServer) CreateNewReport(ctx context.Context, request *lg.NewReportRequest) (*lg.NewReportReply, error) {
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		fmt.Printf("error:%v", err)
		return nil, status.Error(codes.Internal, "Failed to create report")
	}
	query := `INSERT INTO reports (created_by, report_name)
		VALUES ($1, $2)
		RETURNING id;
	`
	sessiondata := ctx.Value("sessionData")
	var reportID int64
	err = conn.QueryRow(ctx, query, sessiondata.(*redisservice.UserSession).UserID, "test").Scan(&reportID)
	if err != nil {
		fmt.Printf("error:%v", err)
		return nil, status.Error(codes.AlreadyExists, "Reports already exist or could not be created")
	}
	conn.Release()
	return &lg.NewReportReply{Reportid: reportID}, nil
}

// endpoint should mainly be used as a refresher,TODO:dont forget rate limits
func (s *loggingWayServer) GetMyCharacters(ctx context.Context, request *lg.GetMyCharactersRequest) (*lg.GetMyCharactersReply, error) {
	sessiondata := ctx.Value("sessionData").(*redisservice.UserSession)
	chars, err := s.xivAuthService.GetCharacters(sessiondata.AccessToken.AccessToken)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Failed to retrieve characters from XIVAuth")
	}
	var charlist []*lg.Character
	for _, char := range chars {
		//For now I am hiding persistent key until I know how I will actually use it
		//Visibility 0 because while working on this project I think I am just gonna remove it at this point
		charlist = append(charlist, &lg.Character{Name: char.Name, Homeworld: char.HomeWorld, Datacenter: char.DataCenter, PersistentKey: "null", Visbility: 0})
	}
	return &lg.GetMyCharactersReply{Characters: charlist}, nil
}

func (s *loggingWayServer) GetMyReports(ctx context.Context, request *lg.GetMyReportsRequest) (*lg.GetMyReportsReply, error) {
	sessiondata := ctx.Value("sessionData").(*redisservice.UserSession)
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to acquire db connection")
	}
	query := `
		SELECT id, created_by, report_name, created_at
		FROM reports
		WHERE created_by = $1 LIMIT 20;
	`
	var reportlist []*lg.Report
	reports, err := conn.Query(ctx, query, sessiondata.UserID)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Report query returned an error")
	}
	for reports.Next() {
		var r Report
		reports.Scan(
			&r.ID,
			&r.CreatedBy,
			&r.ReportName,
			&r.CreatedAt,
		)
		reportlist = append(reportlist, &lg.Report{ReportId: r.ID, ReportName: r.ReportName})
	}
	return &lg.GetMyReportsReply{Reports: reportlist}, nil
}

func (s *loggingWayServer) GetEncountersStats(ctx context.Context, request *lg.GetEncountersStatsRequest) (*lg.GetEncountersStatsReply, error) {
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to acquire db connection")
	}
	query := `
	SELECT * 
	FROM encounter_player_stats
	WHERE encounter_id = $1`
	var encounterslist []*lg.EncounterPlayerBreakdown
	encounters, err := conn.Query(ctx, query, request.EncounterId)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to scan encounter db entry")
	}
	for encounters.Next() {
		var s EncounterPlayerStats
		encounters.Scan(
			&s.ID,
			&s.EncounterID,
			&s.PlayerID,
			&s.PlayerName,
			&s.TotalDamage,
			&s.TotalHealing,
			&s.TotalHits,
			&s.TotalCrits,
			&s.TotalDirectHits,
			&s.FirstTimestamp,
			&s.LastTimestamp,
			&s.DurationSeconds,
			&s.DPS,
			&s.HPS,
			&s.CritRate,
			&s.DirectHitRate,
			&s.CreatedAt,
		)
		encounterslist = append(encounterslist, &lg.EncounterPlayerBreakdown{Name: s.PlayerName,
			TotalDamage:     s.TotalDamage,
			TotalHealing:    s.TotalHealing,
			TotalHits:       s.TotalHits,
			TotalCrits:      s.TotalCrits,
			TotalDirectHits: s.TotalDirectHits,
			Duration:        float32(s.DurationSeconds),
			Dps:             float32(s.DPS),
			Hps:             float32(s.HPS),
			CritRate:        float32(s.CritRate),
			DhRate:          float32(s.DirectHitRate),
		})
	}
	//TODO: check that the requesting user is also the uploader,might be worth restoring uploader field
	return &lg.GetEncountersStatsReply{Playerstats: encounterslist}, nil

}
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", 8085))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	//the redis session service needs to be created outside the grpc server, so we can add
	//the auth function as middleware
	fmt.Println("Initializing Redis session service...")
	redisesion := redisservice.NewRedisSessionService(os.Getenv("REDIS_ADDR"),
		"",
		0,
		"session",
		24*time.Hour,
	)
	fmt.Println("Done")
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
		auth.UnaryServerInterceptor(redisesion.AuthFunc),
	),
		grpc.ChainStreamInterceptor(auth.StreamServerInterceptor(redisesion.AuthFunc)))
	lg.RegisterLoggingwayServer(grpcServer, newServer(redisesion))
	grpcServer.Serve(lis)
}
