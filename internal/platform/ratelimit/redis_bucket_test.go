package ratelimit

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPaymentIntentRateLimit_AllowsWithinBucket(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	for i := 0; i < 10; i++ {
		allowed, err := bucket.Allow(context.Background(), "test:merchant:1", "TIER_1_HIGH_RISK")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Errorf("expected allowed on iteration %d", i)
		}
	}
}

func TestPaymentIntentRateLimit_RejectsWhenBucketEmpty(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	for i := 0; i < 100; i++ {
		_, _ = bucket.Allow(context.Background(), "test:merchant:2", "TIER_1_HIGH_RISK")
	}

	allowed, err := bucket.Allow(context.Background(), "test:merchant:2", "TIER_1_HIGH_RISK")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected rejected when bucket is exhausted")
	}
}

func TestPaymentIntentRateLimit_FailClosedOnRedisError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:9999"})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	allowed, err := bucket.Allow(context.Background(), "test:merchant:3", "TIER_1_HIGH_RISK")
	if err != ErrFailClosed {
		t.Errorf("expected ErrFailClosed, got %v", err)
	}
	if allowed {
		t.Error("expected not allowed on fail closed")
	}
}

func TestPaymentIntentRateLimit_IsolatedByMerchant(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	for i := 0; i < 60; i++ {
		_, _ = bucket.Allow(context.Background(), "test:merchant:A", "TIER_1_HIGH_RISK")
	}

	allowed, err := bucket.Allow(context.Background(), "test:merchant:A", "TIER_1_HIGH_RISK")
	if err != nil || allowed {
		t.Error("merchant A should be rejected after exhausting bucket")
	}

	allowed, err = bucket.Allow(context.Background(), "test:merchant:B", "TIER_1_HIGH_RISK")
	if err != nil || !allowed {
		t.Error("merchant B should be allowed (isolated from A)")
	}
}

func TestRedisBucket_DefaultTierLimitsMatchSpec(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	bucket := NewRedisBucket(rdb, true)

	if got := bucket.getRPS("TIER_1_HIGH_RISK"); got != 10 {
		t.Fatalf("TIER_1_HIGH_RISK RPS = %d, want 10", got)
	}
	if got := bucket.getRPS("TIER_2_COMPLIANT"); got != 50 {
		t.Fatalf("TIER_2_COMPLIANT RPS = %d, want 50", got)
	}
	if got := bucket.getRPS("UNKNOWN_TIER"); got != 50 {
		t.Fatalf("default RPS = %d, want 50", got)
	}
}
