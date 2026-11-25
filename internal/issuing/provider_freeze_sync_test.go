package issuing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── 内存 mock 实现 ──

type mockCardRepo struct {
	mu    sync.Mutex
	cards map[uuid.UUID]*VirtualCard
}

func newMockCardRepo() *mockCardRepo {
	return &mockCardRepo{cards: make(map[uuid.UUID]*VirtualCard)}
}

func (r *mockCardRepo) Create(_ context.Context, card VirtualCard) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cards[card.ID] = &card
	return nil
}

func (r *mockCardRepo) GetByID(_ context.Context, cardID uuid.UUID) (*VirtualCard, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cards[cardID]
	if !ok {
		return nil, ErrCardNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *mockCardRepo) UpdateStatus(_ context.Context, cardID uuid.UUID, status VirtualCardStatus, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cards[cardID]
	if !ok {
		return ErrCardNotFound
	}
	c.Status = status
	if status == CardStatusFrozen {
		c.FrozenReason = reason
	}
	if status == CardStatusTerminated {
		c.TerminatedReason = reason
	}
	return nil
}

func (r *mockCardRepo) GetByIdempotencyKey(_ context.Context, merchantID uuid.UUID, idempotencyKey string) (*VirtualCard, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cards {
		if c.MerchantID == merchantID && c.IdempotencyKey == idempotencyKey {
			cp := *c
			return &cp, nil
		}
	}
	return nil, ErrCardNotFound
}

type mockProviderRegistry struct {
	providers map[uuid.UUID]*IssuingProvider
}

func newMockProviderRegistry() *mockProviderRegistry {
	return &mockProviderRegistry{providers: make(map[uuid.UUID]*IssuingProvider)}
}

func (r *mockProviderRegistry) RegisterProvider(p IssuingProvider) error {
	r.providers[p.ID] = &p
	return nil
}
func (r *mockProviderRegistry) DeregisterProvider(id uuid.UUID) error {
	delete(r.providers, id)
	return nil
}
func (r *mockProviderRegistry) GetProvider(_ context.Context, id uuid.UUID) (*IssuingProvider, error) {
	p, ok := r.providers[id]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return p, nil
}
func (r *mockProviderRegistry) SelectByTierAndFundingMode(_ context.Context, _ *uuid.UUID, _ IssuingTier, _ IssuingFundingMode) ([]IssuingProvider, error) {
	return nil, nil
}
func (r *mockProviderRegistry) GetProviderStatus(id uuid.UUID) (ProviderStatus, bool) {
	p, ok := r.providers[id]
	if !ok {
		return ProviderStatus{}, false
	}
	return ProviderStatus{ProviderID: p.ID, IsActive: p.Status == "ACTIVE"}, true
}
func (r *mockProviderRegistry) Refresh(_ context.Context) error { return nil }
func (r *mockProviderRegistry) ListAllProviders(_ context.Context) ([]IssuingProvider, error) {
	var list []IssuingProvider
	for _, p := range r.providers {
		list = append(list, *p)
	}
	return list, nil
}

type mockVaultSvc struct {
	credential map[string]string
	err        error
}

func (v *mockVaultSvc) Decrypt(_ context.Context, _ VaultCredentialRequest) (map[string]string, error) {
	if v.err != nil {
		return nil, v.err
	}
	return v.credential, nil
}

type mockFreezeAdapter struct {
	freezeCardFunc func(ctx context.Context, req FreezeCardRequest) error
	freezeCount    int32
	lastReq        FreezeCardRequest
}

func (a *mockFreezeAdapter) CreateCard(_ context.Context, _ CreateCardRequest) (*CreateCardResult, error) {
	return nil, nil
}
func (a *mockFreezeAdapter) FreezeCard(ctx context.Context, req FreezeCardRequest) error {
	atomic.AddInt32(&a.freezeCount, 1)
	a.lastReq = req
	if a.freezeCardFunc != nil {
		return a.freezeCardFunc(ctx, req)
	}
	return nil
}
func (a *mockFreezeAdapter) TerminateCard(_ context.Context, _ TerminateCardRequest) error {
	return nil
}
func (a *mockFreezeAdapter) ProcessAuthWebhook(_ context.Context, _ ProcessAuthWebhookRequest) (*CardAuthEvent, error) {
	return nil, nil
}

type mockFreezeAdapterFactory struct {
	adapter IssuingAdapter
	codes   map[string]bool
}

func newMockFreezeAdapterFactory(adapter IssuingAdapter) *mockFreezeAdapterFactory {
	return &mockFreezeAdapterFactory{adapter: adapter, codes: make(map[string]bool)}
}

func (f *mockFreezeAdapterFactory) Create(_ context.Context, providerCode string, _ map[string]string) (IssuingAdapter, error) {
	if !f.codes[providerCode] {
		return nil, ErrProviderCodeUnregistered
	}
	return f.adapter, nil
}
func (f *mockFreezeAdapterFactory) Register(code string, _ func(context.Context, map[string]string) (IssuingAdapter, error)) {
	f.codes[code] = true
}
func (f *mockFreezeAdapterFactory) Has(code string) bool { return f.codes[code] }
func (f *mockFreezeAdapterFactory) Validate() error {
	if len(f.codes) == 0 {
		return fmt.Errorf("no providers registered")
	}
	return nil
}
func (f *mockFreezeAdapterFactory) RegisteredCodes() []string {
	codes := make([]string, 0, len(f.codes))
	for c := range f.codes {
		codes = append(codes, c)
	}
	return codes
}

// mockSyncRepo 是内存 ProviderFreezeSyncRepository 实现。
type mockSyncRepo struct {
	mu         sync.Mutex
	records    map[string]*ProviderFreezeSyncRecord // key = event_id
	callCounts map[string]int32                     // key = event_id
}

func newMockSyncRepo() *mockSyncRepo {
	return &mockSyncRepo{records: make(map[string]*ProviderFreezeSyncRecord), callCounts: make(map[string]int32)}
}

func (r *mockSyncRepo) ClaimOrGetSucceeded(_ context.Context, eventID, idempotencyKey string, cardID, providerID uuid.UUID, providerCardRef, providerRequestKey, lockedBy string, _ time.Duration) (*ProviderFreezeSyncRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.callCounts[eventID]++

	if rec, ok := r.records[eventID]; ok {
		// P0-3: 严格校验幂等重放内容
		if rec.IdempotencyKey != idempotencyKey ||
			rec.CardID != cardID ||
			rec.ProviderID != providerID ||
			rec.ProviderCardRef != providerCardRef ||
			rec.ProviderRequestKey != providerRequestKey {
			return nil, false, fmt.Errorf("issuing: event_id %s reused with different payload: %w", eventID, ErrSyncPayloadMismatch)
		}

		if rec.Status == FreezeSyncSucceeded {
			// 返回副本，不是指针
			cp := *rec
			return &cp, true, nil
		}
		// PROCESSING 状态下检查租约是否仍有效
		if rec.Status == FreezeSyncProcessing && rec.LockedAt != nil {
			if time.Since(*rec.LockedAt) < 2*time.Minute {
				// 租约仍有效，不允许重新认领
				cp := *rec
				return &cp, false, ErrSyncLeaseHeld
			}
		}
		// 重新认领：生成新 claim_token、lease_version+1
		rec.Status = FreezeSyncProcessing
		rec.Attempts++
		rec.ClaimToken = uuid.New()
		rec.LeaseVersion++
		rec.LockedBy = lockedBy
		now := time.Now()
		rec.LockedAt = &now
		// 返回副本，不是指针
		cp := *rec
		return &cp, false, nil
	}

	// R22 Item 7: 模拟 DB UNIQUE 约束冲突检测
	// 按 idempotency_key 或 provider_request_key 查找已有记录
	for _, existing := range r.records {
		if existing.EventID == eventID {
			continue // 同 event_id 已在上面处理
		}
		// 检查 idempotency_key 或 provider_request_key 是否冲突
		if existing.IdempotencyKey == idempotencyKey || existing.ProviderRequestKey == providerRequestKey {
			// 冲突行身份不一致 → ErrSyncPayloadMismatch
			if existing.IdempotencyKey != idempotencyKey ||
				existing.CardID != cardID ||
				existing.ProviderID != providerID ||
				existing.ProviderCardRef != providerCardRef ||
				existing.ProviderRequestKey != providerRequestKey {
				return nil, false, fmt.Errorf("issuing: unique conflict with different identity: %w", ErrSyncPayloadMismatch)
			}
			// 身份一致的冲突行：按 DB 行为返回相应状态
			if existing.Status == FreezeSyncSucceeded {
				cp := *existing
				return &cp, true, nil
			}
			if existing.Status == FreezeSyncProcessing {
				cp := *existing
				return &cp, false, ErrSyncLeaseHeld
			}
			// FAILED/UNKNOWN_EXTERNAL_STATE → reclaimable
			cp := *existing
			return &cp, false, ErrSyncReclaimable
		}
	}

	now := time.Now()
	newClaimToken := uuid.New()
	rec := &ProviderFreezeSyncRecord{
		ID:                 uuid.New(),
		EventID:            eventID,
		IdempotencyKey:     idempotencyKey,
		CardID:             cardID,
		ProviderID:         providerID,
		ProviderCardRef:    providerCardRef,
		ProviderRequestKey: providerRequestKey,
		Status:             FreezeSyncProcessing,
		Attempts:           1,
		LockedBy:           lockedBy,
		LockedAt:           &now,
		ClaimToken:         newClaimToken,
		LeaseVersion:       1,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	r.records[eventID] = rec
	// 返回副本，不是指针
	cp := *rec
	return &cp, false, nil
}

func (r *mockSyncRepo) MarkSucceeded(_ context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[eventID]
	if !ok {
		// DB 契约：RowsAffected == 0 → ErrSyncLeaseLost
		return fmt.Errorf("issuing: record not found: %w", ErrSyncLeaseLost)
	}
	// F-09: 只允许从 PROCESSING 状态标记终态
	// F-02: fencing token 校验
	if rec.Status != FreezeSyncProcessing || rec.ClaimToken != claimToken || rec.LeaseVersion != leaseVersion {
		return fmt.Errorf("issuing: fencing token mismatch: %w", ErrSyncLeaseLost)
	}
	rec.Status = FreezeSyncSucceeded
	now := time.Now()
	rec.CompletedAt = &now
	// F-09: 清除 lease 字段
	rec.ClaimToken = uuid.Nil
	rec.LockedBy = ""
	rec.LockedAt = nil
	return nil
}

func (r *mockSyncRepo) MarkFailed(_ context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[eventID]
	if !ok {
		// DB 契约：RowsAffected == 0 → ErrSyncLeaseLost
		return fmt.Errorf("issuing: record not found: %w", ErrSyncLeaseLost)
	}
	// F-09: 只允许从 PROCESSING 状态标记终态
	// F-02: fencing token 校验
	if rec.Status != FreezeSyncProcessing || rec.ClaimToken != claimToken || rec.LeaseVersion != leaseVersion {
		return fmt.Errorf("issuing: fencing token mismatch: %w", ErrSyncLeaseLost)
	}
	rec.Status = FreezeSyncFailed
	rec.LastError = lastErr
	// F-09: 清除 lease 字段
	rec.ClaimToken = uuid.Nil
	rec.LockedBy = ""
	rec.LockedAt = nil
	return nil
}

func (r *mockSyncRepo) MarkUnknownExternalState(_ context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[eventID]
	if !ok {
		// DB 契约：RowsAffected == 0 → ErrSyncLeaseLost
		return fmt.Errorf("issuing: record not found: %w", ErrSyncLeaseLost)
	}
	// F-09: 只允许从 PROCESSING 状态标记终态
	// F-02: fencing token 校验
	if rec.Status != FreezeSyncProcessing || rec.ClaimToken != claimToken || rec.LeaseVersion != leaseVersion {
		return fmt.Errorf("issuing: fencing token mismatch: %w", ErrSyncLeaseLost)
	}
	rec.Status = FreezeSyncUnknownExternalState
	rec.LastError = lastErr
	// F-09: 清除 lease 字段
	rec.ClaimToken = uuid.Nil
	rec.LockedBy = ""
	rec.LockedAt = nil
	return nil
}

func (r *mockSyncRepo) GetByEventID(_ context.Context, eventID string) (*ProviderFreezeSyncRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[eventID]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return rec, nil
}

// ── 辅助函数 ──

func setupFreezeSyncTest(cardStatus VirtualCardStatus) (*ProviderCardFreezeService, *mockCardRepo, *mockFreezeAdapter, *mockSyncRepo, uuid.UUID, uuid.UUID) {
	cardID := uuid.New()
	providerID := uuid.New()

	cardRepo := newMockCardRepo()
	cardRepo.cards[cardID] = &VirtualCard{
		ID:              cardID,
		ProviderID:      providerID,
		ProviderCardRef: "provider-card-ref-123",
		Status:          cardStatus,
		IdempotencyKey:  "test-idempotency-key",
	}

	registry := newMockProviderRegistry()
	registry.providers[providerID] = &IssuingProvider{
		ID:                 providerID,
		ProviderCode:       "test-provider",
		CredentialVaultKey: "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-0bd4-a716-446655440001/api_key/1",
		AdapterProviderID:  ptrUUID(uuid.New()),
		AdapterType:        AdapterTypeNexusIssuingHTTPV1,
		Status:             "ACTIVE",
	}

	adapter := &mockFreezeAdapter{}
	factory := newMockFreezeAdapterFactory(adapter)
	factory.codes["test-provider"] = true

	vault := &mockVaultSvc{credential: map[string]string{"api_key": "test-key"}}
	syncRepo := newMockSyncRepo()

	svc := NewProviderCardFreezeService(cardRepo, registry, vault, factory, syncRepo, nil)

	return svc, cardRepo, adapter, syncRepo, cardID, providerID
}

// expectedProviderRequestKey 构造期望的 provider_request_key。
func expectedProviderRequestKey(providerID uuid.UUID, providerCardRef, eventID string) string {
	return fmt.Sprintf("freeze:%s:%s:%s", providerID, providerCardRef, eventID)
}

// ── 测试用例 ──

func TestProviderFreezeSync_SuccessKeepsLocalCardFrozen(t *testing.T) {
	svc, cardRepo, adapter, syncRepo, cardID, providerID := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-001",
		IdempotencyKey: "idem-001",
		CardID:         cardID,
		Reason:         "fraud_detected",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)

	// 验证 Provider 被调用
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount), "Provider should be called once")

	// 验证本地卡片状态仍为 FROZEN
	card, _ := cardRepo.GetByID(context.Background(), cardID)
	assert.Equal(t, CardStatusFrozen, card.Status, "local card must remain FROZEN after success")

	// 验证 sync 计为 SUCCEEDED
	rec, _ := syncRepo.GetByEventID(context.Background(), "evt-001")
	assert.Equal(t, FreezeSyncSucceeded, rec.Status)

	// 验证使用了 ProviderCardRef
	assert.Equal(t, "provider-card-ref-123", adapter.lastReq.ProviderCardRef)
	// F-10: IdempotencyKey 应为 provider_request_key，不是内部 outbox key
	assert.Equal(t, expectedProviderRequestKey(providerID, "provider-card-ref-123", "evt-001"), adapter.lastReq.IdempotencyKey)
}

func TestProviderFreezeSync_TimeoutKeepsLocalCardFrozen(t *testing.T) {
	svc, cardRepo, adapter, syncRepo, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)
	adapter.freezeCardFunc = func(_ context.Context, _ FreezeCardRequest) error {
		return ErrProviderTimeout
	}

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-timeout",
		IdempotencyKey: "idem-timeout",
		CardID:         cardID,
		Reason:         "timeout_test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)

	// 验证本地卡片状态仍为 FROZEN（不是 UNKNOWN_EXTERNAL_STATE）
	card, _ := cardRepo.GetByID(context.Background(), cardID)
	assert.Equal(t, CardStatusFrozen, card.Status, "local card must remain FROZEN after timeout")

	// 验证 sync 记为 UNKNOWN_EXTERNAL_STATE
	rec, _ := syncRepo.GetByEventID(context.Background(), "evt-timeout")
	assert.Equal(t, FreezeSyncUnknownExternalState, rec.Status)
}

func TestProviderFreezeSync_FailureKeepsLocalCardFrozen(t *testing.T) {
	svc, cardRepo, adapter, syncRepo, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)
	adapter.freezeCardFunc = func(_ context.Context, _ FreezeCardRequest) error {
		return fmt.Errorf("connection refused")
	}

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-fail",
		IdempotencyKey: "idem-fail",
		CardID:         cardID,
		Reason:         "fail_test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)

	// 验证本地卡片状态仍为 FROZEN
	card, _ := cardRepo.GetByID(context.Background(), cardID)
	assert.Equal(t, CardStatusFrozen, card.Status, "local card must remain FROZEN after failure")

	// 验证 sync 计为 FAILED
	rec, _ := syncRepo.GetByEventID(context.Background(), "evt-fail")
	assert.Equal(t, FreezeSyncFailed, rec.Status)
}

func TestProviderFreezeSync_ReplayDoesNotCallProviderTwice(t *testing.T) {
	svc, cardRepo, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-replay",
		IdempotencyKey: "idem-replay",
		CardID:         cardID,
		Reason:         "replay_test",
	}

	// 第一次处理：成功
	err := svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount), "Provider called once on first attempt")

	// 重放同一事件：应直接返回成功，不再次调用 Provider
	err = svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount), "Provider NOT called on replay (state machine returns success)")

	// 本地卡片仍为 FROZEN
	card, _ := cardRepo.GetByID(context.Background(), cardID)
	assert.Equal(t, CardStatusFrozen, card.Status)
}

func TestProviderFreezeSync_ConcurrentClaimSingleProviderCall(t *testing.T) {
	svc, cardRepo, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-concurrent",
		IdempotencyKey: "idem-concurrent",
		CardID:         cardID,
		Reason:         "concurrent_test",
	}

	var wg sync.WaitGroup
	var successCount int32
	var errCount int32

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.SyncProviderFreeze(context.Background(), req); err != nil {
				atomic.AddInt32(&errCount, 1)
			} else {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// 至少一个成功
	assert.GreaterOrEqual(t, atomic.LoadInt32(&successCount), int32(1), "at least one concurrent attempt should succeed")

	// P0-7 第二轮 P1: Provider 调用次数必须严格等于 1
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount),
		"Provider must be called exactly once across all concurrent attempts")

	// 本地卡片仍为 FROZEN
	card, _ := cardRepo.GetByID(context.Background(), cardID)
	assert.Equal(t, CardStatusFrozen, card.Status)
}

func TestProviderFreezeSync_RetryUsesStableProviderIdempotencyKey(t *testing.T) {
	svc, _, adapter, _, cardID, providerID := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-retry",
		IdempotencyKey: "stable-idem-key",
		CardID:         cardID,
		Reason:         "retry_test",
	}

	// 第一次：超时
	adapter.freezeCardFunc = func(_ context.Context, _ FreezeCardRequest) error {
		return ErrProviderTimeout
	}
	_ = svc.SyncProviderFreeze(context.Background(), req)

	firstReq := adapter.lastReq
	// F-10: IdempotencyKey 应为 provider_request_key
	expectedKey := expectedProviderRequestKey(providerID, "provider-card-ref-123", "evt-retry")
	assert.Equal(t, expectedKey, firstReq.IdempotencyKey, "first attempt uses provider_request_key")

	// 第二次：成功（重试）
	adapter.freezeCardFunc = nil
	err := svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)

	secondReq := adapter.lastReq
	assert.Equal(t, expectedKey, secondReq.IdempotencyKey, "retry uses same provider_request_key")
	assert.Equal(t, firstReq.ProviderCardRef, secondReq.ProviderCardRef, "retry uses same provider_card_ref")
}

func TestProviderFreezeSync_RejectsNonFrozenLocalCard(t *testing.T) {
	// 卡片状态为 ACTIVE（非 FROZEN）
	svc, _, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusActive)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-non-frozen",
		IdempotencyKey: "idem-non-frozen",
		CardID:         cardID,
		Reason:         "test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected FROZEN", "should reject non-FROZEN card")

	// Provider 不应被调用
	assert.Equal(t, int32(0), atomic.LoadInt32(&adapter.freezeCount), "Provider must NOT be called for non-FROZEN card")
}

func TestProviderFreezeSync_RejectsTerminatedCard(t *testing.T) {
	svc, _, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusTerminated)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-terminated",
		IdempotencyKey: "idem-terminated",
		CardID:         cardID,
		Reason:         "test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TERMINATED")

	assert.Equal(t, int32(0), atomic.LoadInt32(&adapter.freezeCount), "Provider must NOT be called for TERMINATED card")
}

func TestProviderFreezeSync_MissingEventIDRejected(t *testing.T) {
	svc, _, _, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "",
		IdempotencyKey: "idem",
		CardID:         cardID,
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EventID")
}

func TestProviderFreezeSync_MissingIdempotencyKeyRejected(t *testing.T) {
	svc, _, _, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt",
		IdempotencyKey: "",
		CardID:         cardID,
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IdempotencyKey")
}

// ── P0-7 第二轮新增测试 ──

// TestProviderFreezeSync_PayloadMismatchDoesNotCallProvider 验证同一 event_id 重放但 payload 不一致时不调用 Provider。
func TestProviderFreezeSync_PayloadMismatchDoesNotCallProvider(t *testing.T) {
	svc, _, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	// 第一次：正常请求
	req1 := ProviderFreezeSyncRequest{
		EventID:        "evt-mismatch",
		IdempotencyKey: "idem-001",
		CardID:         cardID,
		Reason:         "test",
	}
	err := svc.SyncProviderFreeze(context.Background(), req1)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount))

	// 第二次：同一 event_id 但不同 IdempotencyKey → payload mismatch
	req2 := ProviderFreezeSyncRequest{
		EventID:        "evt-mismatch",
		IdempotencyKey: "idem-002", // 不同的幂等键
		CardID:         cardID,
		Reason:         "test",
	}
	err = svc.SyncProviderFreeze(context.Background(), req2)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch)

	// Provider 不应被再次调用
	assert.Equal(t, int32(1), atomic.LoadInt32(&adapter.freezeCount),
		"Provider must NOT be called on payload mismatch")
}

// TestProviderFreezeSync_ExpiredLeaseUsesNewFencingToken 验证过期租约使用新 fencing token。
func TestProviderFreezeSync_ExpiredLeaseUsesNewFencingToken(t *testing.T) {
	svc, _, adapter, syncRepo, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-expired-lease",
		IdempotencyKey: "idem-expired",
		CardID:         cardID,
		Reason:         "test",
	}

	// 第一次：超时
	adapter.freezeCardFunc = func(_ context.Context, _ FreezeCardRequest) error {
		return ErrProviderTimeout
	}
	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err)

	// 检查 sync 记录有 lease_version（claim_token 在终态被清除）
	rec, _ := syncRepo.GetByEventID(context.Background(), "evt-expired-lease")
	assert.Equal(t, int64(1), rec.LeaseVersion, "first attempt lease_version=1")
	assert.Equal(t, FreezeSyncUnknownExternalState, rec.Status, "first attempt status=UNKNOWN_EXTERNAL_STATE")

	// 第二次：成功（重试，过期租约重新认领）
	adapter.freezeCardFunc = nil
	err = svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)

	// 检查 lease_version 递增
	rec, _ = syncRepo.GetByEventID(context.Background(), "evt-expired-lease")
	assert.Equal(t, int64(2), rec.LeaseVersion, "retry must increment lease_version")
	assert.Equal(t, FreezeSyncSucceeded, rec.Status)
}

// TestProviderFreezeSync_OldWorkerCannotOverwriteNewClaim 验证旧 Worker 无法覆盖新 Claim。
func TestProviderFreezeSync_OldWorkerCannotOverwriteNewClaim(t *testing.T) {
	_, _, _, syncRepo, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	// 模拟 Worker A 认领
	providerID := uuid.New()
	syncRec, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-fencing", "idem-fencing",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, syncRec.ClaimToken)
	assert.Equal(t, int64(1), syncRec.LeaseVersion)

	// 保存 Worker A 的 fencing token（mock 返回指针，后续更新会覆盖）
	workerAClaimToken := syncRec.ClaimToken
	workerALeaseVersion := syncRec.LeaseVersion

	// 模拟 Worker B 重新认领（Worker A 的租约过期）
	// 手动使租约过期
	syncRepo.mu.Lock()
	if rec, ok := syncRepo.records["evt-fencing"]; ok {
		pastTime := time.Now().Add(-3 * time.Minute)
		rec.LockedAt = &pastTime
	}
	syncRepo.mu.Unlock()
	syncRec2, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-fencing", "idem-fencing",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), syncRec2.LeaseVersion, "worker-B gets lease_version=2")

	// Worker A 尝试用旧的 claim_token 标记成功 → 应该失败（fencing）
	err = syncRepo.MarkSucceeded(context.Background(), "evt-fencing", workerAClaimToken, workerALeaseVersion)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncLeaseLost)

	// Worker B 用新的 claim_token 标记成功 → 应该成功
	err = syncRepo.MarkSucceeded(context.Background(), "evt-fencing", syncRec2.ClaimToken, syncRec2.LeaseVersion)
	require.NoError(t, err)
}

// F-17: TestProviderFreezeSync_MarkFailureIsReturned 验证 Mark 失败时错误被返回。
// 此测试实际创建 mark failure 场景：在 Provider 调用成功后、MarkSucceeded 之前，
// 另一 Worker 抢占租约（递增 claim_token 和 lease_version），
// 导致当前 Worker 的 MarkSucceeded 因 fencing token 不匹配而失败。
func TestProviderFreezeSync_MarkFailureIsReturned(t *testing.T) {
	svc, _, adapter, syncRepo, cardID, providerID := setupFreezeSyncTest(CardStatusFrozen)

	// 在 Provider 调用成功后、MarkSucceeded 之前，让另一 Worker 抢占租约
	// 使用相同的 payload 以确保 reclaim 成功（递增 claim_token 和 lease_version）
	providerRequestKey := expectedProviderRequestKey(providerID, "provider-card-ref-123", "evt-mark-fail")
	adapter.freezeCardFunc = func(ctx context.Context, _ FreezeCardRequest) error {
		// Provider 调用成功，但在 MarkSucceeded 之前，另一 Worker 重新认领
		// 手动使租约过期以允许 reclaim
		syncRepo.mu.Lock()
		if rec, ok := syncRepo.records["evt-mark-fail"]; ok {
			pastTime := time.Now().Add(-3 * time.Minute)
			rec.LockedAt = &pastTime
		}
		syncRepo.mu.Unlock()
		// 使用相同的 payload 以确保 reclaim 成功
		_, _, _ = syncRepo.ClaimOrGetSucceeded(
			ctx, "evt-mark-fail", "idem-mark-fail",
			cardID, providerID, "provider-card-ref-123", providerRequestKey,
			"worker-B", 2*time.Minute,
		)
		return nil // Provider 调用成功
	}

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-mark-fail",
		IdempotencyKey: "idem-mark-fail",
		CardID:         cardID,
		Reason:         "test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	// MarkSucceeded 应该因 fencing token 不匹配而失败
	require.Error(t, err, "mark failure must be returned as error")
}

// TestProviderFreezeSync_MarkFailedErrorIsReturned 验证 MarkFailed 失败时错误被返回。
// 当 Provider 调用失败后、MarkFailed 之前，另一 Worker 抢占租约，
// 导致 MarkFailed 因 fencing token 不匹配而失败。
func TestProviderFreezeSync_MarkFailedErrorIsReturned(t *testing.T) {
	svc, _, adapter, syncRepo, cardID, providerID := setupFreezeSyncTest(CardStatusFrozen)

	providerRequestKey := expectedProviderRequestKey(providerID, "provider-card-ref-123", "evt-mark-failed")
	// Provider 调用失败，然后在 MarkFailed 之前另一 Worker 抢占
	adapter.freezeCardFunc = func(ctx context.Context, _ FreezeCardRequest) error {
		// 抢占租约（先使租约过期）
		syncRepo.mu.Lock()
		if rec, ok := syncRepo.records["evt-mark-failed"]; ok {
			pastTime := time.Now().Add(-3 * time.Minute)
			rec.LockedAt = &pastTime
		}
		syncRepo.mu.Unlock()
		_, _, _ = syncRepo.ClaimOrGetSucceeded(
			ctx, "evt-mark-failed", "idem-mark-failed",
			cardID, providerID, "provider-card-ref-123", providerRequestKey,
			"worker-B", 2*time.Minute,
		)
		return fmt.Errorf("provider error") // Provider 调用失败
	}

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-mark-failed",
		IdempotencyKey: "idem-mark-failed",
		CardID:         cardID,
		Reason:         "test",
	}

	err := svc.SyncProviderFreeze(context.Background(), req)
	require.Error(t, err, "mark failed error must be returned")
}

// TestProviderFreezeSync_SucceededReplayDoesNotAccessVault 验证已 SUCCEEDED 重放不访问 Vault。
func TestProviderFreezeSync_SucceededReplayDoesNotAccessVault(t *testing.T) {
	svc, _, adapter, _, cardID, _ := setupFreezeSyncTest(CardStatusFrozen)

	req := ProviderFreezeSyncRequest{
		EventID:        "evt-succeeded-replay",
		IdempotencyKey: "idem-succeeded",
		CardID:         cardID,
		Reason:         "test",
	}

	// 第一次：成功
	err := svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err)
	firstCount := atomic.LoadInt32(&adapter.freezeCount)
	assert.Equal(t, int32(1), firstCount)

	// 替换 VaultService 为一个会失败的 mock，验证重放时不访问 Vault
	// 由于 mockSyncRepo 已经标记为 SUCCEEDED，第二次调用应该在 Claim 阶段直接返回
	// 不访问 Vault
	failingVault := &mockVaultSvc{err: ErrCredentialDecryptFailed}
	svc.vaultService = failingVault

	// 第二次：重放已 SUCCEEDED 的记录
	err = svc.SyncProviderFreeze(context.Background(), req)
	require.NoError(t, err, "replay of SUCCEEDED must not access Vault")

	// Provider 调用次数不变
	assert.Equal(t, firstCount, atomic.LoadInt32(&adapter.freezeCount),
		"Provider must NOT be called on SUCCEEDED replay")
}

// ============================================================================
// R22 Item 7: 三条唯一键分别冲突到不同记录时 ErrSyncPayloadMismatch 测试
// ============================================================================

// TestFencingContract_IdempotencyKeyConflictDifferentIdentity 验证不同 event_id 但相同 idempotency_key，
// 不同 payload → ErrSyncPayloadMismatch。
func TestFencingContract_IdempotencyKeyConflictDifferentIdentity(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次插入：event_id="evt-A", idempotency_key="shared-idem"
	_, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-A", "shared-idem",
		cardID, providerID, "ref-001", "freeze:key-A",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 第二次插入：不同 event_id="evt-B"，相同 idempotency_key="shared-idem"，不同 payload
	_, _, err = syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-B", "shared-idem",
		uuid.New(), providerID, "ref-002", "freeze:key-B", // 不同 cardID, providerCardRef, providerRequestKey
		"worker-B", 2*time.Minute,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch)
}

// TestFencingContract_ProviderRequestKeyConflictDifferentIdentity 验证不同 event_id 和 idempotency_key
// 但相同 provider_request_key，不同 payload → ErrSyncPayloadMismatch。
func TestFencingContract_ProviderRequestKeyConflictDifferentIdentity(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次插入：event_id="evt-C", provider_request_key="shared-prk"
	_, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-C", "idem-C",
		cardID, providerID, "ref-001", "shared-prk",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 第二次插入：不同 event_id="evt-D"，不同 idempotency_key="idem-D"，相同 provider_request_key="shared-prk"，不同 payload
	_, _, err = syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-D", "idem-D",
		uuid.New(), providerID, "ref-002", "shared-prk", // 不同 cardID, providerCardRef
		"worker-B", 2*time.Minute,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch)
}

// TestFencingContract_AllThreeKeysSameButDifferentPayload 验证三条唯一键都匹配到不同记录，
// payload 不同 → ErrSyncPayloadMismatch。
func TestFencingContract_AllThreeKeysSameButDifferentPayload(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次插入：event_id="evt-E", idempotency_key="idem-E", provider_request_key="prk-E"
	_, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-E", "idem-E",
		cardID, providerID, "ref-001", "prk-E",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 第二次插入：所有三条唯一键都相同（event_id, idempotency_key, provider_request_key），
	// 但 payload 不同（不同 cardID, providerCardRef）
	// 由于 event_id 相同，这会走 event_id 已存在的分支，检测到 payload mismatch
	_, _, err = syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-E", "idem-E",
		uuid.New(), providerID, "ref-002", "prk-E", // 不同 cardID, providerCardRef
		"worker-B", 2*time.Minute,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch)
}
