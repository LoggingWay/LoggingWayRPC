package redisservice

import (
	"context"
	"fmt"
	"log"
	lg "loggingwayrpc/gen"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type Publisher struct {
	client     *redis.Client
	streamName string
}

type LeaderboardEntry struct {
	Rank        int64
	CharacterID string
	Score       float64
}

func NewPublisher(redisAddr string, streamName string) *Publisher {
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	err := rdb.Ping(context.Background()).Err()
	if err != nil {
		log.Fatalf("could not connect to Redis,SessionClient:%v", err)
	}
	return &Publisher{
		client:     rdb,
		streamName: streamName,
	}
}

func (p *Publisher) PublishEncounter(ctx context.Context, userID uuid.UUID, encounterMsg *lg.NewEncounterRequest) error {
	bytes, err := proto.Marshal(encounterMsg)
	now := time.Now().UTC().Unix()
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		"payload":   bytes,
		"userID":    userID.String(),
		"timestamp": now,
	}
	p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: p.streamName,
		Values: values,
	})
	return nil
}

// The publisher is stateless so I am adding leaderboard to it's responsabilites as it's all redis managed anyway
// Return rank,total,if double zero and no error == not found/deleted
func (p *Publisher) GetEncounterRank(ctx context.Context, characterID uuid.UUID, cfcid uint32) (int64, int64, error) {
	redisKey := fmt.Sprintf("leaderboard:%d", cfcid)
	memberKey := characterID.String()

	rank, err := p.client.ZRevRank(ctx, redisKey, memberKey).Result()

	if err == nil {
		rank = rank + 1                                      //0-based idx to 1 for display
		total, err := p.client.ZCard(ctx, redisKey).Result() //TODO:consult redis doc if there is a way to have both in one cmd instead
		if err == nil {
			return rank, total, nil
		}
	}
	if err != nil && err != redis.Nil { //real error and not just not found/deleted
		return 0, 0, err
	}
	return 0, 0, nil
}

func (p *Publisher) GetEncounterRankPerJob(ctx context.Context, characterID uuid.UUID, cfcid uint32, jobid uint32) (int64, int64, error) {
	redisKey := fmt.Sprintf("leaderboard:%d:%d", cfcid, jobid)
	memberKey := characterID.String()

	rank, err := p.client.ZRevRank(ctx, redisKey, memberKey).Result()

	if err == nil {
		rank = rank + 1                                      //0-based idx to 1 for display
		total, err := p.client.ZCard(ctx, redisKey).Result() //TODO:consult redis doc if there is a way to have both in one cmd instead
		if err == nil {
			return rank, total, nil
		}
	}
	if err != nil && err != redis.Nil { //real error and not just not found/deleted
		return 0, 0, err
	}
	return 0, 0, nil
}

func (p *Publisher) GetLeaderboard(ctx context.Context, cfcID uint32, jobID *uint32) ([]LeaderboardEntry, int64, error) {
	var redisKey string
	if jobID != nil {
		redisKey = fmt.Sprintf("leaderboard:%d:%d", cfcID, *jobID)
	} else {
		redisKey = fmt.Sprintf("leaderboard:%d", cfcID)
	}
	results, err := p.client.ZRevRangeWithScores(ctx, redisKey, 0, 99).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch leaderboard: %w", err)
	}
	total, _ := p.client.ZCard(ctx, redisKey).Result()
	entries := make([]LeaderboardEntry, len(results))
	for i, z := range results {
		entries[i] = LeaderboardEntry{
			CharacterID: z.Member.(string),
			Rank:        int64(i + 1), //0-indexed
			Score:       z.Score,
		}
	}
	return entries, total, nil
}

// Unusued but kept bc might be useful later
/*
func (p *Publisher) PublishBatch(
	ctx context.Context,
	reportID string,
	messages []proto.Message,
) error {

	pipe := p.client.Pipeline()

	now := time.Now().UTC().Unix()

	for _, msg := range messages {

		bytes, err := proto.Marshal(msg)
		if err != nil {
			return err
		}

		values := map[string]interface{}{
			"report_id": reportID,
			"payload":   bytes,
			"timestamp": now,
		}

		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: p.streamName,
			Values: values,
		})
	}

	_, err := pipe.Exec(ctx)
	return err
}
*/
