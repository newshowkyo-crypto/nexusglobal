package riskprofile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type mockRepository struct {
	profiles []*MerchantRiskProfile
	reserves []*RollingReserveEntry
}

func (m *mockRepository) GetByMerchantID(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	for _, p := range m.profiles {
		if p.MerchantID == merchantID {
			return p, nil
		}
	}
	return nil, ErrRiskProfileNotFound
}

func (m *mockRepository) Create(ctx context.Context, profile *MerchantRiskProfile) error {
	m.profiles = append(m.profiles, profile)
	return nil
}

func (m *mockRepository) Update(ctx context.Context, profile *MerchantRiskProfile) error {
	for i, p := range m.profiles {
		if p.ID == profile.ID {
			m.profiles[i] = profile
			return nil
		}
	}
	return ErrRiskProfileNotFound
}

func (m *mockRepository) CreateReserveEntry(ctx context.Context, entry *RollingReserveEntry) error {
	m.reserves = append(m.reserves, entry)
	return nil
}

func (m *mockRepository) GetReserveEntryByID(ctx context.Context, id uuid.UUID) (*RollingReserveEntry, error) {
	for _, r := range m.reserves {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, ErrReserveNotFound
}

func (m *mockRepository) GetReadyToRelease(ctx context.Context, now time.Time) ([]*RollingReserveEntry, error) {
	var ready []*RollingReserveEntry
	for _, r := range m.reserves {
		if r.Status == ReserveStatusHeld && !now.Before(r.ReleaseDate) {
			ready = append(ready, r)
		}
	}
	return ready, nil
}

func (m *mockRepository) UpdateReserveStatus(ctx context.Context, id uuid.UUID, status ReserveStatus) error {
	for i, r := range m.reserves {
		if r.ID == id {
			m.reserves[i].Status = status
			return nil
		}
	}
	return ErrReserveNotFound
}

func (m *mockRepository) GetReservesByMerchant(ctx context.Context, merchantID uuid.UUID) ([]*RollingReserveEntry, error) {
	var result []*RollingReserveEntry
	for _, r := range m.reserves {
		if r.MerchantID == merchantID {
			result = append(result, r)
		}
	}
	return result, nil
}

func TestService_CreateRiskProfile(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	merchantID := uuid.New()
	err := service.CreateRiskProfile(context.Background(), merchantID, 1000, 500, 30)
	if err != nil {
		t.Fatalf("CreateRiskProfile failed: %v", err)
	}

	profile, err := mockRepo.GetByMerchantID(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("GetByMerchantID failed: %v", err)
	}

	if profile.RollingReserveRateBps != 1000 {
		t.Errorf("expected RollingReserveRateBps 1000, got %v", profile.RollingReserveRateBps)
	}
	if profile.DisputeReserveRateBps != 500 {
		t.Errorf("expected DisputeReserveRateBps 500, got %v", profile.DisputeReserveRateBps)
	}
	if profile.ReserveReleaseDays != 30 {
		t.Errorf("expected ReserveReleaseDays 30, got %v", profile.ReserveReleaseDays)
	}
}

func TestService_SplitCaptureAmount(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	merchantID := uuid.New()
	mockRepo.profiles = append(mockRepo.profiles, &MerchantRiskProfile{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		RollingReserveRateBps: 1000,
		DisputeReserveRateBps: 500,
		ReserveReleaseDays:    30,
		Status:                RiskProfileStatusActive,
	})

	reserveAmount, settledAmount, err := service.SplitCaptureAmount(context.Background(), merchantID, 10000)
	if err != nil {
		t.Fatalf("SplitCaptureAmount failed: %v", err)
	}

	expectedReserve := int64(1500)
	expectedSettled := int64(8500)

	if reserveAmount != expectedReserve {
		t.Errorf("expected reserveAmount %v, got %v", expectedReserve, reserveAmount)
	}
	if settledAmount != expectedSettled {
		t.Errorf("expected settledAmount %v, got %v", expectedSettled, settledAmount)
	}
}

func TestService_SplitCaptureAmount_NoProfile(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	merchantID := uuid.New()

	_, _, err := service.SplitCaptureAmount(context.Background(), merchantID, 10000)
	if err != ErrRiskProfileNotFound {
		t.Errorf("expected ErrRiskProfileNotFound, got %v", err)
	}
}

func TestService_ReleaseReserve_Ready(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	entry := &RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    uuid.New(),
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(-1 * time.Hour),
		Status:        ReserveStatusHeld,
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	err := service.ReleaseReserve(context.Background(), entry.ID, false)
	if err != nil {
		t.Fatalf("ReleaseReserve failed: %v", err)
	}

	if entry.Status != ReserveStatusReleased {
		t.Errorf("expected status Released, got %v", entry.Status)
	}
}

func TestService_ReleaseReserve_NotReady(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	entry := &RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    uuid.New(),
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(1 * time.Hour),
		Status:        ReserveStatusHeld,
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	err := service.ReleaseReserve(context.Background(), entry.ID, false)
	if err != ErrReserveNotReady {
		t.Errorf("expected ErrReserveNotReady, got %v", err)
	}
}

func TestService_ReleaseReserve_RiskHold(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	entry := &RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    uuid.New(),
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(-1 * time.Hour),
		Status:        ReserveStatusHeld,
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	err := service.ReleaseReserve(context.Background(), entry.ID, true)
	if err != ErrReserveInRiskHold {
		t.Errorf("expected ErrReserveInRiskHold, got %v", err)
	}
}

func TestService_ReleaseReserve_AlreadyReleased(t *testing.T) {
	mockRepo := &mockRepository{}
	clock := func() time.Time { return time.Now() }
	service := NewService(mockRepo, clock)

	entry := &RollingReserveEntry{
		ID:            uuid.New(),
		MerchantID:    uuid.New(),
		TransactionID: uuid.New(),
		Amount:        1000,
		ReserveType:   ReserveTypeRolling,
		ReleaseDate:   time.Now().Add(-1 * time.Hour),
		Status:        ReserveStatusReleased,
	}
	mockRepo.reserves = append(mockRepo.reserves, entry)

	err := service.ReleaseReserve(context.Background(), entry.ID, false)
	if err != ErrReserveAlreadyReleased {
		t.Errorf("expected ErrReserveAlreadyReleased, got %v", err)
	}
}
