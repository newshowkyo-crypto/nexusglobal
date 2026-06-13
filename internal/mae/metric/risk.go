package metric

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type RiskEngine struct {
	rdb *redis.Client
}

func NewRiskEngine(rdb *redis.Client) *RiskEngine {
	return &RiskEngine{rdb: rdb}
}

func riskKey(maeID, metricType string) string {
	return fmt.Sprintf("RISK:%s:%s", maeID, metricType)
}

func ipKey(ip, metricType string) string {
	return fmt.Sprintf("RISK_IP:%s:%s", ip, metricType)
}

func tenantKey(tenantID, metricType string) string {
	return fmt.Sprintf("RISK_TENANT:%s:%s", tenantID, metricType)
}

func (r *RiskEngine) RecordInvalidCard(ctx context.Context, maeID, ip string) error {
	pipe := r.rdb.Pipeline()
	pipe.Incr(ctx, riskKey(maeID, "invalid_card_count"))
	pipe.Expire(ctx, riskKey(maeID, "invalid_card_count"), 60*time.Second)
	pipe.Incr(ctx, ipKey(ip, "card_count"))
	pipe.Expire(ctx, ipKey(ip, "card_count"), 60*time.Second)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RiskEngine) RecordDecline(ctx context.Context, tenantID string) error {
	pipe := r.rdb.Pipeline()
	pipe.Incr(ctx, tenantKey(tenantID, "decline_count"))
	pipe.Expire(ctx, tenantKey(tenantID, "decline_count"), 60*time.Second)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RiskEngine) RecordCardTesting(ctx context.Context, maeID string) error {
	pipe := r.rdb.Pipeline()
	pipe.Incr(ctx, riskKey(maeID, "card_testing_count"))
	pipe.Expire(ctx, riskKey(maeID, "card_testing_count"), 60*time.Second)
	_, err := pipe.Exec(ctx)
	return err
}

type RiskAssessment struct {
	Rejected bool
	Reason   string
}

func (r *RiskEngine) Assess(ctx context.Context, maeID, ip, tenantID string, invalidCardRateThreshold, uniqueCardCountThreshold, declineCountThreshold, cardTestingThreshold int64) (RiskAssessment, error) {
	pipe := r.rdb.Pipeline()
	invalidCardCmd := pipe.Get(ctx, riskKey(maeID, "invalid_card_count"))
	ipCardCmd := pipe.Get(ctx, ipKey(ip, "card_count"))
	declineCmd := pipe.Get(ctx, tenantKey(tenantID, "decline_count"))
	testingCmd := pipe.Get(ctx, riskKey(maeID, "card_testing_count"))
	_, _ = pipe.Exec(ctx)

	invalidCards, _ := invalidCardCmd.Int64()
	ipCards, _ := ipCardCmd.Int64()
	declines, _ := declineCmd.Int64()
	testingCount, _ := testingCmd.Int64()

	if invalidCardRateThreshold > 0 && invalidCards >= invalidCardRateThreshold {
		return RiskAssessment{Rejected: true, Reason: "invalid_card_rate_60s"}, nil
	}
	if uniqueCardCountThreshold > 0 && ipCards >= uniqueCardCountThreshold {
		return RiskAssessment{Rejected: true, Reason: "unique_card_count_per_ip_60s"}, nil
	}
	if declineCountThreshold > 0 && declines >= declineCountThreshold {
		return RiskAssessment{Rejected: true, Reason: "decline_count_per_tenant_60s"}, nil
	}
	if cardTestingThreshold > 0 && testingCount >= cardTestingThreshold {
		return RiskAssessment{Rejected: true, Reason: "card_testing_count_per_mae_60s"}, nil
	}

	return RiskAssessment{Rejected: false}, nil
}
