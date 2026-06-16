package reserve_release

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nexusglobal/mamr/internal/merchant/riskprofile"
)

// mockRepository 用于测试的mock仓储
type mockRepository struct {
	reserves []*riskprofile.RollingReserveEntry
}

func (m *mockRepository) GetByMerchantID(ctx context.Context, merchantID uuid.UUID) (*riskprofile.MerchantRiskProfile, error) {
	return &riskprofile.MerchantRiskProfile{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		RollingReserveRateBps: 1000,
		DisputeReserveRateBps: 500,
		ReserveReleaseDays:    30,
		Status:                riskprofile.RiskProfileStatusActive,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}, nil
}

func (m *mockRepository) Create(ctx context.Context, profile *riskprofile.MerchantRiskProfile) error {
	return nil
}

func (m *mockRepository) Update(ctx context.Context, profile *riskprofile.MerchantRiskProfile) error {
	return nil
}

func (m *mockRepository) CreateReserveEntry(ctx context.Context, entry *riskprofile.RollingReserveEntry) error {
	m.reserves = append(m.reserves, entry)
	return nil
}

func (m *mockRepository) GetReserveEntryByID(ctx context.Context, id uuid.UUID) (*riskprofile.RollingReserveEntry, error) {
	for _, r := range m.reserves {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, riskprofile.ErrReserveNotFound
}

func (m *mockRepository) GetReadyToRelease(ctx context.Context, now time.Time) ([]*riskprofile.RollingReserveEntry, error) {
	var ready []*riskprofile.RollingReserveEntry
	for _, r := range m.reserves {
		if r.Status == riskprofile.ReserveStatusHeld && !now.Before(r.ReleaseDate) {
			ready = append(ready, r)
		}
	}
	return ready, nil
}

func (m *mockRepository) UpdateReserveStatus(ctx context.Context, id uuid.UUID, status riskprofile.ReserveStatus) error {
	for _, r := range m.reserves {
		if r.ID == id {
			r.Status = status
			return nil
		}
	}
	return riskprofile.ErrReserveNotFound
}

func (m *mockRepository) GetReservesByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*riskprofile.RollingReserveEntry, error) {
	var result []*riskprofile.RollingReserveEntry
	for _, r := range m.reserves {
		if r.MerchantID == merchantID {
			result = append(result, r)
		}
	}
	return result, nil
}

func TestWorker_ProcessReserves(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	riskProfileSvc := riskprofile.NewService(mockRepo, clock)

	logger := slog.Default()
	worker := NewWorker(riskProfileSvc, logger, time.Minute)

	// 创建一个已到期的准备金条目
	merchantID := uuid.New()
	entry := &riskprofile.RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    merchantID,
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   riskprofile.ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(-1 * time.Hour), // 已到期
		Status:        riskprofile.ReserveStatusHeld,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	// 处理准备金
	err := worker.processReserves(context.Background())
	if err != nil {
		t.Fatalf("processReserves failed: %v", err)
	}

	// 验证准备金状态已更新
	if entry.Status != riskprofile.ReserveStatusReleased {
		t.Errorf("expected status Released, got %v", entry.Status)
	}
}

func TestWorker_NotReadyReserves(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	riskProfileSvc := riskprofile.NewService(mockRepo, clock)

	logger := slog.Default()
	worker := NewWorker(riskProfileSvc, logger, time.Minute)

	// 创建一个未到期的准备金条目
	merchantID := uuid.New()
	entry := &riskprofile.RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    merchantID,
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   riskprofile.ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(1 * time.Hour), // 未到期
		Status:        riskprofile.ReserveStatusHeld,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	// 处理准备金（不应释放）
	err := worker.processReserves(context.Background())
	if err != nil {
		t.Fatalf("processReserves failed: %v", err)
	}

	// 验证准备金状态未改变
	if entry.Status != riskprofile.ReserveStatusHeld {
		t.Errorf("expected status Held, got %v", entry.Status)
	}
}
