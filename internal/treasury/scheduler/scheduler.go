package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type DelayQueue struct {
	rdb   *redis.Client
	key   string
}

func NewDelayQueue(rdb *redis.Client, key string) *DelayQueue {
	return &DelayQueue{rdb: rdb, key: key}
}

func (q *DelayQueue) Enqueue(ctx context.Context, ruleID string, executeAt time.Time) error {
	return q.rdb.ZAdd(ctx, q.key, redis.Z{
		Score:  float64(executeAt.Unix()),
		Member: ruleID,
	}).Err()
}

func (q *DelayQueue) DequeueReady(ctx context.Context, limit int64) ([]string, error) {
	now := float64(time.Now().Unix())

	members, err := q.rdb.ZRangeByScore(ctx, q.key, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%f", now),
		Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}

	if len(members) == 0 {
		return nil, nil
	}

	membersInterface := make([]interface{}, len(members))
	for i, m := range members {
		membersInterface[i] = m
	}
	q.rdb.ZRem(ctx, q.key, membersInterface...)

	return members, nil
}

func (q *DelayQueue) Size(ctx context.Context) (int64, error) {
	return q.rdb.ZCard(ctx, q.key).Result()
}

func (q *DelayQueue) Clear(ctx context.Context) error {
	return q.rdb.Del(ctx, q.key).Err()
}
