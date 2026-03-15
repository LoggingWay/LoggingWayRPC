package redisservice

import (
	"context"
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
		"userID":    userID,
		"timestamp": now,
	}
	p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: p.streamName,
		Values: values,
	})
	return nil
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
