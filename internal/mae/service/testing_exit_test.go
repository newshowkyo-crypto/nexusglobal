package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/mae/domain"
	"github.com/nexusglobal/mamr/internal/mae/repository/db"
)

// recordingRepo 记录 UpdateStatus 的最后一次调用，用于断言 service 层真实行为。
type recordingRepo struct {
	config       *db.MaeConfiguration
	lastStatus   string
	lastReason   string
	updateCalled bool
}

func (r *recordingRepo) InsertConfig(_ context.Context, _ db.InsertMAEConfigParams) (*db.MaeConfiguration, error) {
	return nil, nil
}
func (r *recordingRepo) GetConfig(_ context.Context, _ uuid.UUID) (*db.MaeConfiguration, error) {
	return r.config, nil
}
func (r *recordingRepo) GetConfigByMAEID(_ context.Context, _ string) (*db.MaeConfiguration, error) {
	return r.config, nil
}
func (r *recordingRepo) ListConfigs(_ context.Context) ([]db.MaeConfiguration, error) {
	if r.config == nil {
		return nil, nil
	}
	return []db.MaeConfiguration{*r.config}, nil
}
func (r *recordingRepo) UpdateStatus(_ context.Context, _ uuid.UUID, status, reason string, _ int32, _ *time.Time) error {
	r.updateCalled = true
	r.lastStatus = status
	r.lastReason = reason
	return nil
}
func (r *recordingRepo) RotateSecret(_ context.Context, _ uuid.UUID, _, _ []byte, _ int32) error {
	return nil
}

func (r *recordingRepo) UpdateConfig(_ context.Context, _ db.UpdateMAEConfigParams) error {
	return nil
}

// memFailCounter 内存版连续失败计数器，模拟 Redis 行为。
type memFailCounter struct {
	counts map[string]int32
}

func newMemFailCounter() *memFailCounter {
	return &memFailCounter{counts: map[string]int32{}}
}
func (m *memFailCounter) GetTestingFailCount(_ context.Context, maeID string) (int32, error) {
	return m.counts[maeID], nil
}
func (m *memFailCounter) IncrTestingFailCount(_ context.Context, maeID string) (int32, error) {
	m.counts[maeID]++
	return m.counts[maeID], nil
}
func (m *memFailCounter) ResetTestingFailCount(_ context.Context, maeID string) error {
	delete(m.counts, maeID)
	return nil
}

func testingConfig() *db.MaeConfiguration {
	return &db.MaeConfiguration{
		ID:                       uuid.New(),
		MaeID:                    "mae_test",
		Status:                   string(domain.StatusTesting),
		TestingMinTxnCount:       100,
		TestingMinSuccessRateBps: 9500,
		CmaxBps:                  200,
		TestingMaxP95LatencyMs:   1000,
		CooldownMinutes:          30,
		Version:                  1,
	}
}

// T1-2: 连续灰度失败 3 次 → Suspended（验证 service 层真实读取 Redis 计数并传入纯函数）。
func TestEvaluateTesting_ConsecutiveFailures_Suspend(t *testing.T) {
	ctx := context.Background()
	repo := &recordingRepo{config: testingConfig()}
	fc := newMemFailCounter()
	svc := New(repo, nil, nil).WithFailCounter(fc)

	// 不达标的指标（TxnCount 远低于 min），每次评估都应判 fail。
	failingCmd := EvaluateTestingCmd{
		MAEID:          "mae_test",
		TxnCount:       10,
		SuccessRateBps: 9000,
	}

	// 第 1 次失败 → HOLD（回 Cooldown），计数 1
	res1, err := svc.EvaluateTesting(ctx, failingCmd)
	require.NoError(t, err)
	assert.Equal(t, domain.TestingHold, res1.Decision)
	assert.Equal(t, int32(1), fc.counts["mae_test"])

	// 第 2 次失败 → HOLD，计数 2
	res2, err := svc.EvaluateTesting(ctx, failingCmd)
	require.NoError(t, err)
	assert.Equal(t, domain.TestingHold, res2.Decision)
	assert.Equal(t, int32(2), fc.counts["mae_test"])

	// 第 3 次失败 → 此时 priorFailCount=2，纯函数 failCount=3 >= 3 → Suspend，计数被清零
	res3, err := svc.EvaluateTesting(ctx, failingCmd)
	require.NoError(t, err)
	assert.Equal(t, domain.TestingSuspend, res3.Decision)
	assert.Equal(t, string(domain.StatusSuspended), repo.lastStatus)
	assert.Equal(t, int32(0), fc.counts["mae_test"], "suspend 后应清零计数")
}

// T1-2: 达标 → Active，且失败计数清零。
func TestEvaluateTesting_Promote_ResetsCounter(t *testing.T) {
	ctx := context.Background()
	repo := &recordingRepo{config: testingConfig()}
	fc := newMemFailCounter()
	fc.counts["mae_test"] = 1 // 先前有一次失败
	svc := New(repo, nil, nil).WithFailCounter(fc)

	passingCmd := EvaluateTestingCmd{
		MAEID:           "mae_test",
		TxnCount:        150,
		SuccessRateBps:  9800,
		AbnormalRateBps: 10,
		P95LatencyMs:    500,
	}

	res, err := svc.EvaluateTesting(ctx, passingCmd)
	require.NoError(t, err)
	assert.Equal(t, domain.TestingPromote, res.Decision)
	assert.Equal(t, string(domain.StatusActive), repo.lastStatus)
	assert.Equal(t, int32(0), fc.counts["mae_test"], "promote 后应清零计数")
}

// T1-1: 探测窗口超时 → 回 Cooldown。
func TestEvaluateTestingTimeout_RevertsToCooldown(t *testing.T) {
	ctx := context.Background()
	cfg := testingConfig()
	// 进入 Testing 的时间设为 1 小时前，cooldown_minutes=30 → 已超时
	cfg.UpdatedAt = pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true}
	repo := &recordingRepo{config: cfg}
	fc := newMemFailCounter()
	fc.counts["mae_test"] = 2
	svc := New(repo, nil, nil).WithFailCounter(fc)

	timedOut, err := svc.EvaluateTestingTimeout(ctx, "mae_test", time.Now())
	require.NoError(t, err)
	assert.True(t, timedOut)
	assert.Equal(t, string(domain.StatusCooldown), repo.lastStatus)
	assert.Equal(t, "TESTING_WINDOW_TIMEOUT", repo.lastReason)
	assert.Equal(t, int32(0), fc.counts["mae_test"], "超时后应清零计数")
}

// T1-1: 未超时则不迁移。
func TestEvaluateTestingTimeout_WithinWindow_NoChange(t *testing.T) {
	ctx := context.Background()
	cfg := testingConfig()
	// 刚进入 Testing（5 分钟前），cooldown_minutes=30 → 未超时
	cfg.UpdatedAt = pgtype.Timestamptz{Time: time.Now().Add(-5 * time.Minute), Valid: true}
	repo := &recordingRepo{config: cfg}
	svc := New(repo, nil, nil)

	timedOut, err := svc.EvaluateTestingTimeout(ctx, "mae_test", time.Now())
	require.NoError(t, err)
	assert.False(t, timedOut)
	assert.False(t, repo.updateCalled, "未超时不应调用 UpdateStatus")
}
