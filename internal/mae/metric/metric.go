package metric

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Aggregator struct {
	rdb *redis.Client
}

func NewAggregator(rdb *redis.Client) *Aggregator {
	return &Aggregator{rdb: rdb}
}

func exposureKey(maeID, businessDate string) string {
	return fmt.Sprintf("MAE_METRIC:%s:%s:gross_exposure_amount", maeID, businessDate)
}

func capturedKey(maeID, businessDate string) string {
	return fmt.Sprintf("MAE_METRIC:%s:%s:captured_amount", maeID, businessDate)
}

func refundKey(maeID, businessDate string) string {
	return fmt.Sprintf("MAE_METRIC:%s:%s:refund_amount", maeID, businessDate)
}

func disputeKey(maeID, businessDate string) string {
	return fmt.Sprintf("MAE_METRIC:%s:%s:dispute_amount", maeID, businessDate)
}

func (a *Aggregator) OnCaptured(ctx context.Context, maeID, businessDate string, amountMinor int64) error {
	pipe := a.rdb.Pipeline()
	pipe.IncrBy(ctx, exposureKey(maeID, businessDate), amountMinor)
	pipe.IncrBy(ctx, capturedKey(maeID, businessDate), amountMinor)
	_, err := pipe.Exec(ctx)
	return err
}

func (a *Aggregator) OnRefund(ctx context.Context, maeID, businessDate string, amountMinor int64) error {
	return a.rdb.IncrBy(ctx, refundKey(maeID, businessDate), amountMinor).Err()
}

func (a *Aggregator) OnDispute(ctx context.Context, maeID, businessDate string, amountMinor int64) error {
	return a.rdb.IncrBy(ctx, disputeKey(maeID, businessDate), amountMinor).Err()
}

func (a *Aggregator) GetExposure(ctx context.Context, maeID, businessDate string) (int64, error) {
	return a.rdb.Get(ctx, exposureKey(maeID, businessDate)).Int64()
}

func (a *Aggregator) GetCaptured(ctx context.Context, maeID, businessDate string) (int64, error) {
	return a.rdb.Get(ctx, capturedKey(maeID, businessDate)).Int64()
}

func (a *Aggregator) GetRefund(ctx context.Context, maeID, businessDate string) (int64, error) {
	return a.rdb.Get(ctx, refundKey(maeID, businessDate)).Int64()
}

func (a *Aggregator) GetDispute(ctx context.Context, maeID, businessDate string) (int64, error) {
	return a.rdb.Get(ctx, disputeKey(maeID, businessDate)).Int64()
}

type Snapshot struct {
	MAEID              string
	BusinessDate       string
	GrossExposureMinor int64
	CapturedMinor      int64
	RefundMinor        int64
	DisputeMinor       int64
	AbnormalRateBps    int32
	P95LatencyMs       int32
	CardTestingCount   int32
}

func (a *Aggregator) TakeSnapshot(ctx context.Context, maeID, businessDate string) (Snapshot, error) {
	pipe := a.rdb.Pipeline()
	exposureCmd := pipe.Get(ctx, exposureKey(maeID, businessDate))
	capturedCmd := pipe.Get(ctx, capturedKey(maeID, businessDate))
	refundCmd := pipe.Get(ctx, refundKey(maeID, businessDate))
	disputeCmd := pipe.Get(ctx, disputeKey(maeID, businessDate))
	_, _ = pipe.Exec(ctx)

	exposure, _ := exposureCmd.Int64()
	captured, _ := capturedCmd.Int64()
	refund, _ := refundCmd.Int64()
	dispute, _ := disputeCmd.Int64()

	return Snapshot{
		MAEID:              maeID,
		BusinessDate:       businessDate,
		GrossExposureMinor: exposure,
		CapturedMinor:      captured,
		RefundMinor:        refund,
		DisputeMinor:       dispute,
	}, nil
}

func FormatBusinessDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
