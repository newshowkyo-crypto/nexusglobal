package pool

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/pool/domain"
)

func testChannel(merchantID uuid.UUID) domain.Channel {
	return domain.Channel{
		ID:                uuid.New(),
		TenantID:          merchantID,
		MerchantID:        merchantID,
		AcquirerChannelID: uuid.New(),
		BusinessLines:     []string{"digital"},
		Currencies:        []string{"USD"},
		CardBrands:        []string{"visa", "mastercard"},
		MinAmountMinor:    100,
		MaxAmountMinor:    1_000_000,
		DailyLimitMinor:   50_000,
		CostBps:           250,
		SettlementDays:    2,
		Weight:            100,
		HealthScore:       9500,
		Status:            "ACTIVE",
	}
}

func TestFilter_HardRules(t *testing.T) {
	merchantID := uuid.New()
	base := testChannel(merchantID)
	req := domain.SelectionRequest{
		MerchantID:   merchantID,
		BusinessLine: "digital",
		Currency:     "USD",
		CardBrand:    "visa",
		AmountMinor:  10_000,
	}

	assert.Len(t, Filter(req, []domain.Channel{base}, nil), 1)

	t.Run("wrong currency", func(t *testing.T) {
		r := req
		r.Currency = "EUR"
		assert.Empty(t, Filter(r, []domain.Channel{base}, nil))
	})

	t.Run("business line not approved", func(t *testing.T) {
		r := req
		r.BusinessLine = "gambling"
		assert.Empty(t, Filter(r, []domain.Channel{base}, nil))
	})

	t.Run("amount below minimum", func(t *testing.T) {
		r := req
		r.AmountMinor = 50
		assert.Empty(t, Filter(r, []domain.Channel{base}, nil))
	})

	t.Run("daily limit exhausted", func(t *testing.T) {
		c := base
		c.Watermark.UsedAmountMinor = 45_000
		c.Watermark.ReservedAmountMinor = 4_000
		assert.Empty(t, Filter(req, []domain.Channel{c}, nil))
	})

	t.Run("monthly limit exhausted", func(t *testing.T) {
		c := base
		c.MonthlyLimitMinor = 20_000
		usage := map[[16]byte]int64{c.ID: 19_000}
		assert.Empty(t, Filter(req, []domain.Channel{c}, usage))
	})

	t.Run("disabled channel", func(t *testing.T) {
		c := base
		c.Status = "TRIPPED"
		assert.Empty(t, Filter(req, []domain.Channel{c}, nil))
	})
}

func TestScore_PrefersHealthyAndCheap(t *testing.T) {
	merchantID := uuid.New()
	good := testChannel(merchantID)
	bad := testChannel(merchantID)
	bad.HealthScore = 5000
	bad.CostBps = 450
	bad.SettlementDays = 7

	req := domain.SelectionRequest{MerchantID: merchantID, BusinessLine: "digital", Currency: "USD", AmountMinor: 10_000}
	w := DefaultWeights()
	assert.Greater(t, Score(good, req, w), Score(bad, req, w))
}

func TestPickWeighted_Deterministic(t *testing.T) {
	a := domain.Candidate{Weight: 3}
	b := domain.Candidate{Weight: 1}
	list := []domain.Candidate{a, b}

	assert.Equal(t, &list[0], PickWeighted(list, 0.0))
	assert.Equal(t, &list[1], PickWeighted(list, 0.99))
}

func TestParseMode(t *testing.T) {
	assert.Equal(t, domain.ModeOff, domain.ParseMode(""))
	assert.Equal(t, domain.ModeOff, domain.ParseMode("bogus"))
	assert.Equal(t, domain.ModeShadow, domain.ParseMode("shadow"))
	assert.Equal(t, domain.ModeEnforce, domain.ParseMode("enforce"))
}

// fakeStore 内存实现，用于分配器测试。
type fakeStore struct {
	policy   *domain.Policy
	channels []domain.Channel
	usage    map[uuid.UUID]int64
	// reserveFail 模拟通道额度被并发占满
	reserveFail map[uuid.UUID]bool
	monthFail   map[uuid.UUID]bool
	leases      []domain.Lease
	standby     *domain.Lease
}

func (f *fakeStore) GetPolicy(_ context.Context, _ uuid.UUID, _, _ string) (*domain.Policy, error) {
	return f.policy, nil
}

func (f *fakeStore) ListChannels(_ context.Context, ids []uuid.UUID) ([]domain.Channel, error) {
	var out []domain.Channel
	for _, c := range f.channels {
		for _, id := range ids {
			if c.ID == id {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) GetChannel(_ context.Context, id uuid.UUID) (*domain.Channel, error) {
	for _, c := range f.channels {
		if c.ID == id {
			cp := c
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) MonthUsage(_ context.Context, id uuid.UUID, _ time.Time) (int64, error) {
	return f.usage[id], nil
}

func (f *fakeStore) ReserveWatermark(_ context.Context, id uuid.UUID, _ time.Time, _ int64, _ int64) (bool, error) {
	return !f.reserveFail[id], nil
}

func (f *fakeStore) ReserveMonth(_ context.Context, id uuid.UUID, _ time.Time, _ int64, _ int64) (bool, error) {
	return !f.monthFail[id], nil
}

func (f *fakeStore) ConfirmWatermark(_ context.Context, _ uuid.UUID, _ time.Time, _ int64) error {
	return nil
}

func (f *fakeStore) ReleaseWatermark(_ context.Context, _ uuid.UUID, _ time.Time, _ int64) error {
	return nil
}

func (f *fakeStore) InsertLeases(_ context.Context, leases []domain.Lease) error {
	f.leases = append(f.leases, leases...)
	return nil
}

func (f *fakeStore) SetLeaseState(_ context.Context, _, _ uuid.UUID, _ string) error { return nil }

func (f *fakeStore) NextStandby(_ context.Context, _ uuid.UUID) (*domain.Lease, error) {
	if f.standby == nil {
		return nil, domain.ErrLeaseNotFound
	}
	return f.standby, nil
}

func contextBackground() context.Context { return context.Background() }

func TestFilterKeepsOnlySiteAuthorizedRuntimeChannels(t *testing.T) {
	allowed, blocked := uuid.New(), uuid.New()
	candidates := []domain.Channel{
		{ID: uuid.New(), AcquirerChannelID: blocked, Status: "ACTIVE"},
		{ID: uuid.New(), AcquirerChannelID: allowed, Status: "ACTIVE"},
	}
	req := domain.SelectionRequest{Currency: "USD", AmountMinor: 100, AllowedAcquirerChannels: map[uuid.UUID]bool{allowed: true}}
	result := Filter(req, candidates, nil)
	if len(result) != 1 || result[0].AcquirerChannelID != allowed {
		t.Fatalf("unexpected site channel candidates: %v", result)
	}
	req.AllowedAcquirerChannels = map[uuid.UUID]bool{}
	if result := Filter(req, candidates, nil); len(result) != 0 {
		t.Fatalf("an empty site authorization must fail closed: %v", result)
	}
}

func TestService_Select_Enforce(t *testing.T) {
	merchantID := uuid.New()
	c1 := testChannel(merchantID)
	c2 := testChannel(merchantID)
	c2.HealthScore = 7000

	p := &domain.Policy{
		MerchantID:        merchantID,
		AllowedChannelIDs: []uuid.UUID{c1.ID, c2.ID},
		Strategy:          "WEIGHTED",
		Status:            "ACTIVE",
	}
	store := &fakeStore{policy: p, channels: []domain.Channel{c1, c2}, reserveFail: map[uuid.UUID]bool{}}
	svc := New(store, domain.ModeEnforce, nil)
	svc.SetRollForTest(func() float64 { return 0.0 }) // 命中第一个候选

	decision, err := svc.Select(contextBackground(), domain.SelectionRequest{
		MerchantID: merchantID, BusinessLine: "digital", Currency: "USD", AmountMinor: 10_000,
		AttemptID: uuid.New(),
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Contains(t, []uuid.UUID{c1.AcquirerChannelID, c2.AcquirerChannelID}, decision.AcquirerChannelID)
	assert.Equal(t, domain.ModeEnforce, decision.Mode)
	assert.NotEmpty(t, store.leases, "must persist held + standby leases")
	assert.Equal(t, domain.LeaseHeld, store.leases[0].State)
}

func TestService_Select_FailClosed(t *testing.T) {
	merchantID := uuid.New()
	c1 := testChannel(merchantID)

	// 无策略
	svc := New(&fakeStore{}, domain.ModeEnforce, nil)
	_, err := svc.Select(contextBackground(), domain.SelectionRequest{
		MerchantID: merchantID, Currency: "USD", AmountMinor: 1000,
	})
	assert.ErrorIs(t, err, domain.ErrNoPolicy)

	// 有策略但全部被过滤
	store := &fakeStore{
		policy:      &domain.Policy{MerchantID: merchantID, AllowedChannelIDs: []uuid.UUID{c1.ID}},
		channels:    []domain.Channel{c1},
		reserveFail: map[uuid.UUID]bool{},
	}
	svc = New(store, domain.ModeEnforce, nil)
	_, err = svc.Select(contextBackground(), domain.SelectionRequest{
		MerchantID: merchantID, Currency: "EUR", AmountMinor: 1000,
	})
	assert.ErrorIs(t, err, domain.ErrNoCandidate)
}

func TestService_Select_LimitExceeded(t *testing.T) {
	merchantID := uuid.New()
	c1 := testChannel(merchantID)
	store := &fakeStore{
		policy:      &domain.Policy{MerchantID: merchantID, AllowedChannelIDs: []uuid.UUID{c1.ID}},
		channels:    []domain.Channel{c1},
		reserveFail: map[uuid.UUID]bool{c1.ID: true},
	}
	svc := New(store, domain.ModeEnforce, nil)
	_, err := svc.Select(contextBackground(), domain.SelectionRequest{
		MerchantID: merchantID, Currency: "USD", BusinessLine: "digital", AmountMinor: 10_000,
	})
	assert.ErrorIs(t, err, domain.ErrLimitExceeded)
}

func TestService_Failover(t *testing.T) {
	merchantID := uuid.New()
	c1 := testChannel(merchantID)
	attemptID := uuid.New()
	store := &fakeStore{
		channels: []domain.Channel{c1},
		standby: &domain.Lease{
			ChannelID: c1.ID, AttemptID: attemptID, Rank: 2, State: domain.LeaseStandby,
		},
	}
	svc := New(store, domain.ModeEnforce, nil)
	decision, err := svc.Failover(contextBackground(), attemptID)
	require.NoError(t, err)
	assert.Equal(t, c1.AcquirerChannelID, decision.AcquirerChannelID)
}
