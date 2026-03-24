package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	lg "loggingwayrpc/gen"
	redisservice "loggingwayrpc/redis"
	"loggingwayrpc/xivauth"
	"net"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noevain/xivauthgo"
	"github.com/rs/zerolog"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
	Logger         *zerolog.Logger
}

type Report struct {
	ID         int64
	CreatedBy  uuid.UUID // pointer because it can be NULL
	ReportName string
	CreatedAt  time.Time
}

type charDataLeaderboard struct {
	Name       string
	Homeworld  string
	Datacenter string
	AvatarURL  *string
}

// for now, no point in exposing payload
type EncounterNoPayload struct {
	ID          int64
	zone_id     int32
	uploaded_at time.Time
	uploaded_by uuid.UUID
}

type EncounterPlayerStats struct {
	ID              int64
	EncounterID     int64
	uploaded_by     uuid.UUID
	character       uuid.UUID
	PlayerID        int64
	PlayerName      string
	JobId           int32
	TotalDamage     int64
	TotalPscore     float32
	TotalHealing    int64
	TotalHits       int64
	TotalCrits      int64
	TotalDirectHits int64
	FirstTimestamp  int64
	LastTimestamp   int64
	DurationSeconds float32
	DPS             float32
	HPS             float32
	CritRate        float32
	DirectHitRate   float32
	CreatedAt       time.Time
	CfcId           int32 //PGSQL does not store unsigned value
}

/*
TODO: create a pgx db service
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
func newServer() *loggingWayServer {
	fmt.Println("Initializing xivauth service...")
	xivs, err := xivauth.NewService(os.Getenv("XIVAUTH_CLIENT_ID"), os.Getenv("XIVAUTH_CLIENT_SECRET"),
		os.Getenv("XIVAUTH_LOGIN_CALLBACK"),
		[]string{
			xivauth.ScopeUser,
			xivauth.ScopeCharacterAll,
			xivauth.ScopeRefresh,
		})
	if err != nil {
		log.Fatalf("Failed to initialize xivauthservice: %v", err)
	}

	fmt.Println("Initializing redis related services...")
	rediserv := redisservice.NewPublisher(os.Getenv("REDIS_ADDR"), "reports_stream")
	redisesion := redisservice.NewRedisSessionService(os.Getenv("REDIS_ADDR"),
		"",
		0,
		"session",
		168*time.Hour, //session live for 1 week
		xivs,
	)
	redistate := redisservice.NewRedisStateStoreService(os.Getenv("REDIS_ADDR"),
		"",
		0,
		"state",
		1*time.Hour)
	connPool, err := pgxpool.NewWithConfig(context.Background(), pgxconfidk())
	if err != nil {
		log.Fatalf("Failed to create database conn pool:%v", err)
	}
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
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
		connpool:       connPool,
		Logger:         &logger}
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
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to acquire db connection")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:       pgx.Serializable,
		AccessMode:     pgx.ReadWrite,
		DeferrableMode: pgx.NotDeferrable,
	})
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to create transaction")
	}
	defer tx.Rollback(ctx) //auto-fails if tx is commited before this fct returns
	query := `INSERT INTO users (xivauthid, username, verified_characters)
    VALUES ($1, $2, $3)
    ON CONFLICT (xivauthid) DO UPDATE SET xivauthid = EXCLUDED.xivauthid
    RETURNING id;`
	//this query is a hack,the update clause will do nothing but if you do DO NOTHING and there is conflict, it will return 0000-0000-0etc.. which
	//mean you'd have to do another query to check the user... this allows to just do 1 and have both
	var myuserID uuid.UUID
	err = tx.QueryRow(ctx, query, user.ID, "None", true).Scan(&myuserID)
	if err != nil { //if conflict,no row returned so we can skip the character insert
		s.Logger.Err(err).Msg("Error while inserting/updating user")
		return nil, status.Error(codes.Internal, "Failed to insert/update user")
	}
	sessionData := redisservice.UserSession{
		UserID:      myuserID.String(),
		XivAuthID:   user.ID,
		AccessToken: *token,
		Characters:  character,
	}
	sessionID := uuid.NewString()
	err = s.Sessioner.CreateSession(ctx, sessionID, sessionData)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to create session")
	}

	query_char := `INSERT INTO characters_claim (xivauthkey,claim_by,lodestone_id,avatar_url,portrait_url,charname,datacenter,homeworld)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (xivauthkey,charname,datacenter,homeworld) DO NOTHING;`
	for _, char := range sessionData.Characters {
		_, err := tx.Exec(ctx, query_char, char.PersistentKey, myuserID, char.LodestoneID, char.AvatarURL, char.PortraitURL, char.Name, char.DataCenter, char.HomeWorld)
		if err != nil {
			s.Logger.Err(err).Msg("Error while inserting characters_claim")
		}
	}
	//NOTE:I created EnrollCharacter but it's unused for now, so there is no way to add a character without logging out then in
	//TODO: actually implement it lmao
	tx.Commit(ctx)
	return &lg.LoginReply{SessionID: sessionID}, nil
}

func (s *loggingWayServer) Logout(ctx context.Context, request *lg.LogoutRequest) (*lg.LogoutReply, error) {
	md, _ := metadata.FromIncomingContext(ctx) //impossible for this to fail,middleware would have before
	token := md["authorization"]
	err := s.Sessioner.DeleteSession(ctx, token[0])
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Session was either not found or already dead")
	}
	return &lg.LogoutReply{}, nil
}

// endpoint should mainly be used as a refresher,TODO:dont forget rate limits
func (s *loggingWayServer) GetMyCharacters(ctx context.Context, request *lg.GetMyCharactersRequest) (*lg.GetMyCharactersReply, error) {
	sessiondata := ctx.Value("sessionData").(*redisservice.UserSession)
	//TODO:move Token management to a middleware,have the AccessToken exposed via ctx instead
	tksource := s.oauthConfig.TokenSource(ctx, &sessiondata.AccessToken)
	fmt.Println("Refresh token:", sessiondata.AccessToken.RefreshToken)
	token, err := tksource.Token()
	if err != nil {
		return nil, status.Error(codes.Aborted, "Token expired or revoked")
	}
	chars, err := s.xivAuthService.GetCharacters(token.AccessToken)
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

func (s *loggingWayServer) GetMyEncounters(ctx context.Context, request *lg.GetMyEncountersRequest) (*lg.GetMyEncountersReply, error) {
	sessiondata := ctx.Value("sessionData").(*redisservice.UserSession)
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		fmt.Printf("error:%v", err)
		return nil, status.Error(codes.Internal, "Failed to get encounters")
	}
	defer conn.Release()
	var query string
	var encounters pgx.Rows
	var encounterslist []*lg.Encounter
	if request.ZoneId == 0 {
		query = `SELECT id,cfc_id,uploaded_at,uploaded_by
	FROM encounters
	WHERE uploaded_by = $1 LIMIT 20;`
		encounters, err = conn.Query(ctx, query, sessiondata.UserID)
		if err != nil {
			fmt.Println(err)
			return nil, status.Error(codes.NotFound, "Failed to read encounters")
		}
	} else {
		query = `SELECT id,cfc_id,uploaded_at,uploaded_by
	FROM encounters
	WHERE uploaded_by = $1 AND cfc_id = $2 LIMIT 20;`
		encounters, err = conn.Query(ctx, query, sessiondata.UserID, request.ZoneId)
		if err != nil {
			fmt.Println(err)
			return nil, status.Error(codes.NotFound, "Failed to read encounters")
		}
	}
	for encounters.Next() {
		var s EncounterNoPayload
		if err := encounters.Scan(
			&s.ID,
			&s.zone_id,
			&s.uploaded_at,
			&s.uploaded_by,
		); err != nil {
			fmt.Println("scan error:", err)
			continue
		}
		encounterslist = append(encounterslist, &lg.Encounter{
			EncounterId: s.ID,
			ZoneId:      uint32(s.zone_id),
			UploadedAt:  s.uploaded_at.Unix(),
		})
	}
	return &lg.GetMyEncountersReply{Encounters: encounterslist}, nil
}
func (s *loggingWayServer) EncounterIngest(ctx context.Context, request *lg.NewEncounterRequest) (*lg.NewEncounterReply, error) {
	sessiondata := ctx.Value("sessionData")
	userid := uuid.MustParse(sessiondata.(*redisservice.UserSession).UserID)
	if err := s.Publisher.PublishEncounter(ctx, userid, request); err != nil {
		return nil, status.Error(codes.Canceled, "Error submiting report")
	}
	return &lg.NewEncounterReply{Code: 0}, nil
}
func (s *loggingWayServer) GetEncountersStats(ctx context.Context, request *lg.GetEncountersStatsRequest) (*lg.GetEncountersStatsReply, error) {
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, "Failed to acquire db connection")
	}
	defer conn.Release()
	sessiondata := ctx.Value("sessionData")
	userid := uuid.MustParse(sessiondata.(*redisservice.UserSession).UserID)
	query := `
SELECT eps.encounter_id, eps.uploaded_by, eps.character, eps.player_id, eps.player_name, 
       eps.job_id, eps.total_damage, eps.total_pscore, eps.total_healing, eps.total_hits,
       eps.total_crits, eps.total_direct_hits, eps.first_timestamp, eps.last_timestamp, 
       eps.duration_seconds, eps.dps, eps.hps, eps.crit_rate, eps.direct_hit_rate, 
       eps.created_at, e.cfc_id
FROM encounter_player_stats eps
JOIN encounters e ON eps.encounter_id = e.id
WHERE eps.encounter_id = $1` //This query is fairly heavy and could use a little work but should be fine for a while
	var encounterResponse lg.EncounterPlayerBreakdown
	var queried EncounterPlayerStats
	//I know, could just scan the entire object into the response, but in the future we want to tighly control what the client is allowed to see
	err = conn.QueryRow(ctx, query, request.EncounterId).Scan(
		&queried.EncounterID,
		&queried.uploaded_by,
		&queried.character,
		&queried.PlayerID,
		&queried.PlayerName,
		&queried.JobId,
		&queried.TotalDamage,
		&queried.TotalPscore,
		&queried.TotalHealing,
		&queried.TotalHits,
		&queried.TotalCrits,
		&queried.TotalDirectHits,
		&queried.FirstTimestamp,
		&queried.LastTimestamp,
		&queried.DurationSeconds,
		&queried.DPS,
		&queried.HPS,
		&queried.CritRate,
		&queried.DirectHitRate,
		&queried.CreatedAt,
		&queried.CfcId)
	if err != nil {
		fmt.Println("scan error:", err)
		return nil, status.Error(codes.NotFound, "Encounter ID not found")
	}
	if queried.uploaded_by != userid {
		return nil, status.Error(codes.PermissionDenied, "Naughty little boy/girl/whatever(UserID does not match requested encounter)")
	}
	encounterResponse.Name = queried.PlayerName
	encounterResponse.CritRate = queried.CritRate
	encounterResponse.TotalDamage = queried.TotalDamage
	encounterResponse.Dps = queried.DPS
	encounterResponse.DhRate = queried.DirectHitRate
	encounterResponse.Duration = queried.DurationSeconds
	encounterResponse.Pscore = queried.TotalPscore
	encounterResponse.TotalHits = queried.TotalHits
	encounterResponse.TotalCrits = queried.TotalCrits
	encounterResponse.TotalDirectHits = queried.TotalDirectHits
	encounterResponse.JobId = uint32(queried.JobId)
	rank, total, err := s.Publisher.GetEncounterRankPerJob(ctx, queried.character, uint32(queried.CfcId), uint32(queried.JobId))
	if err != nil { //err is not just redis.Nil
		return nil, status.Error(codes.Unknown, "Leaderboard lookup failed very hard")
	}
	encounterResponse.Rank = rank
	encounterResponse.TotalRanked = total
	global, tglobal, err := s.Publisher.GetEncounterRank(ctx, queried.character, uint32(queried.CfcId))
	if err != nil {
		return nil, status.Error(codes.Unknown, "Leaderboard lookup failed very hard")
	}
	encounterResponse.GlobalRank = global
	encounterResponse.GlobalTotalRank = tglobal
	return &lg.GetEncountersStatsReply{Playerstats: &encounterResponse}, nil

}

func (s *loggingWayServer) GetLeaderBoard(ctx context.Context, request *lg.GetLeaderBoardRequest) (*lg.GetLeaderBoardReply, error) {
	conn, err := s.connpool.Acquire(ctx)
	if err != nil {
		fmt.Printf("error:%v", err)
		return nil, status.Error(codes.Internal, "Failed to get leaderboard")
	}
	defer conn.Release()
	var jobid *uint32
	if request.JobId != 0 {
		jobid = &request.JobId
	}
	entries, total, err := s.Publisher.GetLeaderboard(ctx, request.ZoneId, jobid)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to pull leaderboard info")
	}
	characterIDs := make([]string, len(entries))
	for i, e := range entries {
		characterIDs[i] = e.CharacterID
	}
	rows, err := conn.Query(ctx, `
        SELECT id, charname, homeworld, datacenter
        FROM characters_claim
        WHERE id = ANY($1)`,
		characterIDs)
	if err != nil {
		fmt.Println("failed to enrich leaderboard: %w", err)
		return nil, status.Error(codes.Internal, "Failed to pull leaderboard info(pgsql)")
	}
	defer rows.Close()
	charMap := make(map[string]charDataLeaderboard)
	for rows.Next() {
		var id string
		var c charDataLeaderboard
		if err := rows.Scan(&id, &c.Name, &c.Homeworld, &c.Datacenter); err != nil {
			return nil, status.Error(codes.Internal, "Failed to bind character and leaderboard data?")
		}
		charMap[id] = c
	}
	var ent []*lg.LeaderBoardEntry
	for _, e := range entries {
		if c, ok := charMap[e.CharacterID]; ok {
			ent = append(ent, &lg.LeaderBoardEntry{
				Char:    &lg.Character{Name: c.Name, Homeworld: c.Homeworld, Datacenter: c.Datacenter},
				Rank:    e.Rank,
				Psccore: float32(e.Score),
				Jobid:   request.JobId}) //GetLeaderboard return the approriate result
		}

	}
	return &lg.GetLeaderBoardReply{Entry: ent, TotalRanked: total}, nil

}
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", 8085))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	logway := newServer()
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			auth.UnaryServerInterceptor(logway.Sessioner.AuthFunc),
		),
		grpc.ChainStreamInterceptor(
			auth.StreamServerInterceptor(logway.Sessioner.AuthFunc),
		),
	)
	lg.RegisterLoggingwayServer(grpcServer, logway)
	grpcServer.Serve(lis)
}
