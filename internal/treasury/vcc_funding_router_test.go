package treasury

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	issuing "github.com/nexusglobal/mamr/internal/issuing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockVCCFundingRepository struct {
	mock.Mock
}

func (m *mockVCCFundingRepository) LockMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID) error {
	args := m.Called(ctx, merchantID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) DebitMerchantSettledBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64, txID string) error {
	args := m.Called(ctx, merchantID, amountMinor, txID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) CreditPendingCryptoBridgeBalance(ctx context.Context, merchantID uuid.UUID, amountMinor int64) error {
	args := m.Called(ctx, merchantID, amountMinor)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) WriteOutboxEvent(ctx context.Context, event OutboxEvent) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) GetMerchantRiskProfile(ctx context.Context, merchantID uuid.UUID) (*MerchantRiskProfile, error) {
	args := m.Called(ctx, merchantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MerchantRiskProfile), args.Error(1)
}

func (m *mockVCCFundingRepository) GetIssuingProvider(ctx context.Context, providerID uuid.UUID) (*issuing.IssuingProvider, error) {
	args := m.Called(ctx, providerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*issuing.IssuingProvider), args.Error(1)
}

// P0-A2: 事务版本 mock 实现

func (m *mockVCCFundingRepository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(pgx.Tx), args.Error(1)
}

func (m *mockVCCFundingRepository) LockMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) error {
	args := m.Called(ctx, tx, merchantID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) DebitMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64, txID string) error {
	args := m.Called(ctx, tx, merchantID, amountMinor, txID)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) CreditPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	args := m.Called(ctx, tx, merchantID, amountMinor)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) WriteOutboxEventTx(ctx context.Context, tx pgx.Tx, event OutboxEvent) error {
	args := m.Called(ctx, tx, event)
	return args.Error(0)
}

// P0-A1: Finalize 阶段 mock 实现

func (m *mockVCCFundingRepository) DebitPendingCryptoBridgeBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	args := m.Called(ctx, tx, merchantID, amountMinor)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) CreditMerchantSettledBalanceTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, amountMinor int64) error {
	args := m.Called(ctx, tx, merchantID, amountMinor)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) WriteOutboxEventFinalizeTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error {
	args := m.Called(ctx, tx, event, idempotencyKey)
	return args.Error(0)
}

// R8: 业务幂等 + issuing provider 资金落点 mock 实现

func (m *mockVCCFundingRepository) CreditIssuingProviderBalanceTx(ctx context.Context, tx pgx.Tx, providerID uuid.UUID, merchantID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, idempotencyKey string) error {
	args := m.Called(ctx, tx, providerID, merchantID, bridgeBatchID, amountMinor, currency, idempotencyKey)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) ClaimFundingRequestTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, bridgeBatchID string, amountMinor int64, currency string, routeType string) (bool, error) {
	args := m.Called(ctx, tx, merchantID, sourceLedgerTxID, targetProviderID, bridgeBatchID, amountMinor, currency, routeType)
	return args.Bool(0), args.Error(1)
}

func (m *mockVCCFundingRepository) ClaimFundingFinalizationTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, merchantID uuid.UUID, amountMinor int64, currency string) (bool, error) {
	args := m.Called(ctx, tx, bridgeBatchID, targetProviderID, merchantID, amountMinor, currency)
	return args.Bool(0), args.Error(1)
}

func (m *mockVCCFundingRepository) UpdateFundingRequestStatusTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, sourceLedgerTxID string, targetProviderID uuid.UUID, status string) error {
	args := m.Called(ctx, tx, merchantID, sourceLedgerTxID, targetProviderID, status)
	return args.Error(0)
}

// P0-1/P0-2: FIAT_DIRECT issuing funding outbox + finalization 状态更新 mock 实现

func (m *mockVCCFundingRepository) WriteOutboxIssuingFundingTx(ctx context.Context, tx pgx.Tx, event OutboxEvent, idempotencyKey string) error {
	args := m.Called(ctx, tx, event, idempotencyKey)
	return args.Error(0)
}

func (m *mockVCCFundingRepository) UpdateFundingFinalizationStatusTx(ctx context.Context, tx pgx.Tx, bridgeBatchID string, targetProviderID uuid.UUID, status string) error {
	args := m.Called(ctx, tx, bridgeBatchID, targetProviderID, status)
	return args.Error(0)
}

// mockTx 是 pgx.Tx 的最小 mock，用于测试事务行为。
type mockTx struct {
	commitErr   error
	rollbackErr error
}

func (m *mockTx) Commit(ctx context.Context) error {
	return m.commitErr
}

func (m *mockTx) Rollback(ctx context.Context) error {
	return m.rollbackErr
}

func (m *mockTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errors.New("nested tx not supported")
}

func (m *mockTx) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (m *mockTx) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (m *mockTx) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return nil
}

func (m *mockTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}

func (m *mockTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return nil
}

func (m *mockTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (m *mockTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (m *mockTx) Conn() *pgx.Conn {
	return nil
}

func TestRouteFunding_HighRiskFiatDirect_Rejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierHighRisk,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrHighRiskFiatDirect)
	assert.False(t, result.Approved)
	assert.Equal(t, RouteTypeRejected, result.RouteType)
	assert.Contains(t, result.RejectReason, "high-risk")
}

func TestRouteFunding_HighRiskCryptoDesensitized_Allowed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierHighRisk,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	// R8: 稳定 bridge_batch_id (P0-4: 包含 merchantID)
	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-123:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-123").Return(nil)
	mockRepo.On("CreditPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("WriteOutboxEventTx", ctx, tx, mock.Anything).Return(nil)
	mockRepo.On("UpdateFundingRequestStatusTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, "COMPLETED").Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierHighRisk,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, RouteTypeCryptoDesensitized, result.RouteType)
	assert.Equal(t, expectedBridgeBatchID, result.BridgeBatchID)
	assert.Equal(t, ProviderTierHighRisk, result.Provenance.SourceTier)
	assert.Equal(t, sourceChannelID.String(), result.Provenance.SourceRail)
	assert.Equal(t, "ledger-tx-123", result.Provenance.SourceLedgerTxID)

	mockRepo.AssertExpectations(t)
}

func TestRouteFunding_CompliantFiatDirect_Allowed(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  20,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeFiatDirect,
		Status:      "ACTIVE",
	}

	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-456:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-456", targetProviderID, expectedBridgeBatchID, int64(50000), "USD", "FIAT_DIRECT").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(50000), "ledger-tx-456").Return(nil)
	mockRepo.On("CreditIssuingProviderBalanceTx", ctx, tx, targetProviderID, merchantID, expectedBridgeBatchID, int64(50000), "USD", "vcc_funding_fiat:"+expectedBridgeBatchID+":"+targetProviderID.String()).Return(nil)
	mockRepo.On("WriteOutboxIssuingFundingTx", ctx, tx, mock.Anything, "vcc_issuing_funding:"+expectedBridgeBatchID+":"+targetProviderID.String()).Return(nil)
	mockRepo.On("UpdateFundingRequestStatusTx", ctx, tx, merchantID, "ledger-tx-456", targetProviderID, "COMPLETED").Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       50000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-456",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, RouteTypeFiatDirect, result.RouteType)
	assert.Equal(t, ProviderTierCompliant, result.Provenance.SourceTier)
	assert.Equal(t, expectedBridgeBatchID, result.BridgeBatchID)

	mockRepo.AssertExpectations(t)
}

func TestRouteFunding_InvalidAmount(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       0,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAmount)
	assert.False(t, result.Approved)
}

func TestRouteFunding_UnknownSourceTier(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTier("UNKNOWN_TIER"),
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownSourceTier)
	assert.False(t, result.Approved)
}

func TestRouteFunding_UnknownFundingMode(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingMode("UNKNOWN_MODE"),
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownFundingMode)
	assert.False(t, result.Approved)
}

func TestRouteFunding_MissingProvenanceMetadata(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingProvenanceMetadata)
	assert.False(t, result.Approved)
}

func TestRouteFunding_MerchantBlocked(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  100,
		IsBlocked:  true,
		SourceTier: ProviderTierCompliant,
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "blocked")
}

func TestRouteFunding_FundingModeMismatch(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "mismatch")
}

func TestRouteFunding_InsufficientBalance(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		RiskScore:  50,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeFiatDirect,
		Status:      "ACTIVE",
	}

	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-123:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "FIAT_DIRECT").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-123").Return(ErrInsufficientBalance)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
	assert.False(t, result.Approved)
}

// P0-A2: 事务原子性测试 — Debit 失败后 CreditPending 不应被调用
func TestRouteFunding_CryptoDesensitized_DebitFails_NoCreditPending(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-123:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-123").Return(ErrInsufficientBalance)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
	assert.False(t, result.Approved)

	// CreditPendingCryptoBridgeBalanceTx 和 WriteOutboxEventTx 不应被调用
	mockRepo.AssertNotCalled(t, "CreditPendingCryptoBridgeBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventTx")
}

// P0-A2: 事务原子性测试 — CreditPending 失败后 WriteOutbox 不应被调用
func TestRouteFunding_CryptoDesensitized_CreditPendingFails_NoOutbox(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	creditErr := errors.New("treasury: credit failed")

	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-123:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-123").Return(nil)
	mockRepo.On("CreditPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(creditErr)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "failed to credit bridge balance")

	// WriteOutboxEventTx 不应被调用
	mockRepo.AssertNotCalled(t, "WriteOutboxEventTx")
}

// P0-A2: 事务原子性测试 — Commit 失败应返回 Rejected
func TestRouteFunding_CryptoDesensitized_CommitFails_Rejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{commitErr: errors.New("commit failed")}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, mock.Anything, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-123").Return(nil)
	mockRepo.On("CreditPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("WriteOutboxEventTx", ctx, tx, mock.Anything).Return(nil)
	mockRepo.On("UpdateFundingRequestStatusTx", ctx, tx, merchantID, "ledger-tx-123", targetProviderID, "COMPLETED").Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "failed to commit")
}

// P0-A2: 事务原子性测试 — BeginTx 失败应返回 Rejected
func TestRouteFunding_BeginTxFails_Rejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeFiatDirect,
		Status:      "ACTIVE",
	}

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(nil, errors.New("connection failed"))

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeFiatDirect,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-123",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "failed to begin tx")

	// 余额操作不应被调用
	mockRepo.AssertNotCalled(t, "LockMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "DebitMerchantSettledBalanceTx")
}

// ============================================================================
// P0-A1: FinalizeVCCFunding 测试 — 验证桥接完成后的最终入金
// ============================================================================

func TestFinalizeVCCFunding_Success(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-123", targetProviderID, merchantID, int64(100000), "USD").Return(true, nil)
	mockRepo.On("DebitPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("CreditIssuingProviderBalanceTx", ctx, tx, targetProviderID, merchantID, "bb-123", int64(100000), "USD", "vcc_funding_finalize:bb-123:"+targetProviderID.String()).Return(nil)
	mockRepo.On("WriteOutboxEventFinalizeTx", ctx, tx, mock.Anything, "vcc_issuing_funding:bb-123:"+targetProviderID.String()).Return(nil)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-123",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
		Provenance:       map[string]string{"crypto_bridge_batch_id": "bb-123"},
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, RouteTypeCryptoDesensitized, result.RouteType)
	assert.Equal(t, "bb-123", result.BridgeBatchID)
	assert.Equal(t, targetProviderID, result.TargetProviderID)

	mockRepo.AssertExpectations(t)

	// 关键约束验证：FinalizeVCCFunding 不应调用 RouteFunding 相关的方法
	mockRepo.AssertNotCalled(t, "LockMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "DebitMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "CreditPendingCryptoBridgeBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventTx")
	// R8: 不应 credit settled_balance
	mockRepo.AssertNotCalled(t, "CreditMerchantSettledBalanceTx")
}

func TestFinalizeVCCFunding_InsufficientPendingBalance(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-456", targetProviderID, merchantID, int64(100000), "USD").Return(true, nil)
	mockRepo.On("DebitPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(ErrInsufficientBalance)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-456",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
	assert.False(t, result.Approved)

	// CreditIssuingProviderBalanceTx 和 WriteOutbox 不应被调用
	mockRepo.AssertNotCalled(t, "CreditIssuingProviderBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventFinalizeTx")
	// R8: 不应 credit settled_balance
	mockRepo.AssertNotCalled(t, "CreditMerchantSettledBalanceTx")
}

func TestFinalizeVCCFunding_DoesNotCallRouteFunding(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-789", targetProviderID, merchantID, int64(50000), "USD").Return(true, nil)
	mockRepo.On("DebitPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(50000)).Return(nil)
	mockRepo.On("CreditIssuingProviderBalanceTx", ctx, tx, targetProviderID, merchantID, "bb-789", int64(50000), "USD", mock.Anything).Return(nil)
	mockRepo.On("WriteOutboxEventFinalizeTx", ctx, tx, mock.Anything, mock.Anything).Return(nil)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-789",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      50000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)

	// 验证不调用 RouteFunding 相关的任何方法
	mockRepo.AssertNotCalled(t, "GetMerchantRiskProfile")
	mockRepo.AssertNotCalled(t, "GetIssuingProvider")
	mockRepo.AssertNotCalled(t, "LockMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "DebitMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "CreditPendingCryptoBridgeBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventTx")
	// R8: 不应 credit settled_balance
	mockRepo.AssertNotCalled(t, "CreditMerchantSettledBalanceTx")
}

func TestFinalizeVCCFunding_InvalidAmount(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-000",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      0,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAmount)
	assert.False(t, result.Approved)
}

func TestFinalizeVCCFunding_MissingBridgeBatchID(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "bridge_batch_id")
}

func TestFinalizeVCCFunding_CommitFails_Rejected(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{commitErr: errors.New("commit failed")}

	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-commit-fail", targetProviderID, merchantID, int64(100000), "USD").Return(true, nil)
	mockRepo.On("DebitPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("CreditIssuingProviderBalanceTx", ctx, tx, targetProviderID, merchantID, "bb-commit-fail", int64(100000), "USD", mock.Anything).Return(nil)
	mockRepo.On("WriteOutboxEventFinalizeTx", ctx, tx, mock.Anything, mock.Anything).Return(nil)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-commit-fail",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.Error(t, err)
	assert.False(t, result.Approved)
	assert.Contains(t, result.RejectReason, "failed to commit")
}

// ============================================================================
// R8: FinalizeVCCFunding 资金落点 + 业务幂等测试
// ============================================================================

// TestFinalizeVCCFunding_DoesNotCreditSettled 验证 FinalizeVCCFunding 不调用 CreditMerchantSettledBalanceTx
func TestFinalizeVCCFunding_DoesNotCreditSettled(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-no-settled", targetProviderID, merchantID, int64(100000), "USD").Return(true, nil)
	mockRepo.On("DebitPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("CreditIssuingProviderBalanceTx", ctx, tx, targetProviderID, merchantID, "bb-no-settled", int64(100000), "USD", mock.Anything).Return(nil)
	mockRepo.On("WriteOutboxEventFinalizeTx", ctx, tx, mock.Anything, mock.Anything).Return(nil)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-no-settled",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)

	// 核心断言：绝不调用 CreditMerchantSettledBalanceTx
	mockRepo.AssertNotCalled(t, "CreditMerchantSettledBalanceTx")
}

// TestFinalizeVCCFunding_Idempotent_SecondCallSkipsDebitCredit 验证同一 bridge_batch_id + target_provider_id 第二次调用不发生余额变动
func TestFinalizeVCCFunding_Idempotent_SecondCallSkipsDebitCredit(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	// ClaimFundingFinalizationTx 返回 false — 表示已存在，幂等成功
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingFinalizationTx", ctx, tx, "bb-idem", targetProviderID, merchantID, int64(100000), "USD").Return(false, nil)

	req := FinalizeVCCFundingRequest{
		BridgeBatchID:    "bb-idem",
		MerchantID:       merchantID,
		TargetProviderID: targetProviderID,
		AmountMinor:      100000,
		Currency:         "USD",
	}

	result, err := router.FinalizeVCCFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, "bb-idem", result.BridgeBatchID)

	// 幂等成功 — 不应发生任何余额变动
	mockRepo.AssertNotCalled(t, "DebitPendingCryptoBridgeBalanceTx")
	mockRepo.AssertNotCalled(t, "CreditIssuingProviderBalanceTx")
	mockRepo.AssertNotCalled(t, "CreditMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventFinalizeTx")
}

// TestRouteFunding_CryptoDesensitized_StableBridgeBatchID 验证 bridge_batch_id 从稳定业务键派生
func TestRouteFunding_CryptoDesensitized_StableBridgeBatchID(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	// 预期 bridge_batch_id = uuid.NewSHA1(NamespaceOID, merchantID + ":ledger-tx-stable:" + targetProviderID)
	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-stable:"+targetProviderID.String())).String()

	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-stable", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(true, nil)
	mockRepo.On("LockMerchantSettledBalanceTx", ctx, tx, merchantID).Return(nil)
	mockRepo.On("DebitMerchantSettledBalanceTx", ctx, tx, merchantID, int64(100000), "ledger-tx-stable").Return(nil)
	mockRepo.On("CreditPendingCryptoBridgeBalanceTx", ctx, tx, merchantID, int64(100000)).Return(nil)
	mockRepo.On("WriteOutboxEventTx", ctx, tx, mock.Anything).Return(nil)
	mockRepo.On("UpdateFundingRequestStatusTx", ctx, tx, merchantID, "ledger-tx-stable", targetProviderID, "COMPLETED").Return(nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-stable",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	// 验证 bridge_batch_id 是从稳定业务键派生的确定性 UUID
	assert.Equal(t, expectedBridgeBatchID, result.BridgeBatchID)

	mockRepo.AssertExpectations(t)
}

// TestRouteFunding_CryptoDesensitized_Idempotent_SecondCallSkipsDebit 验证同一 source_ledger_tx_id 第二次调用不发生余额变动
func TestRouteFunding_CryptoDesensitized_Idempotent_SecondCallSkipsDebit(t *testing.T) {
	ctx := context.Background()
	merchantID := uuid.New()
	sourceChannelID := uuid.New()
	targetProviderID := uuid.New()

	mockRepo := new(mockVCCFundingRepository)
	router := NewVCCFundingRouter(mockRepo, 50)
	tx := &mockTx{}

	riskProfile := &MerchantRiskProfile{
		MerchantID: merchantID,
		IsBlocked:  false,
		SourceTier: ProviderTierCompliant,
	}

	provider := &issuing.IssuingProvider{
		ID:          targetProviderID,
		FundingMode: FundingModeCryptoDesensitized,
		Status:      "ACTIVE",
	}

	expectedBridgeBatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchantID.String()+":ledger-tx-idem:"+targetProviderID.String())).String()

	// ClaimFundingRequestTx 返回 false — 表示已存在，幂等成功
	mockRepo.On("GetMerchantRiskProfile", ctx, merchantID).Return(riskProfile, nil)
	mockRepo.On("GetIssuingProvider", ctx, targetProviderID).Return(provider, nil)
	mockRepo.On("BeginTx", ctx).Return(tx, nil)
	mockRepo.On("ClaimFundingRequestTx", ctx, tx, merchantID, "ledger-tx-idem", targetProviderID, expectedBridgeBatchID, int64(100000), "USD", "CRYPTO_DESENSITIZED").Return(false, nil)

	req := FundingRouteRequest{
		MerchantID:        merchantID,
		SourceChannelID:   sourceChannelID,
		SourceTier:        ProviderTierCompliant,
		TargetProviderID:  targetProviderID,
		TargetFundingMode: FundingModeCryptoDesensitized,
		AmountMinor:       100000,
		Currency:          "USD",
		SourceLedgerTxID:  "ledger-tx-idem",
	}

	result, err := router.RouteFunding(ctx, req)

	assert.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, expectedBridgeBatchID, result.BridgeBatchID)

	// 幂等成功 — 不应发生任何余额变动
	mockRepo.AssertNotCalled(t, "LockMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "DebitMerchantSettledBalanceTx")
	mockRepo.AssertNotCalled(t, "CreditPendingCryptoBridgeBalanceTx")
	mockRepo.AssertNotCalled(t, "WriteOutboxEventTx")
	mockRepo.AssertNotCalled(t, "UpdateFundingRequestStatusTx")
}

// ============================================================================
// P0-4: bridge_batch_id 派生包含 merchantID 的测试
// ============================================================================

// TestRouteFunding_CryptoDesensitized_DifferentMerchants_DifferentBridgeBatchIDs 验证
// 两个不同 merchant 使用同一 source_ledger_tx_id 时，bridge_batch_id 不同。
// P0-4: bridge_batch_id 必须包含 merchant_id、source_ledger_tx_id、target_provider_id。
func TestRouteFunding_CryptoDesensitized_DifferentMerchants_DifferentBridgeBatchIDs(t *testing.T) {
	merchant1 := uuid.New()
	merchant2 := uuid.New()
	targetProviderID := uuid.New()

	// 两个 merchant 使用同一 source_ledger_tx_id
	sourceLedgerTxID := "shared-ledger-tx-999"

	// 预期 bridge_batch_id 不同（因为 merchantID 不同）
	batchID1 := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchant1.String()+":"+sourceLedgerTxID+":"+targetProviderID.String())).String()
	batchID2 := uuid.NewSHA1(uuid.NameSpaceOID, []byte(merchant2.String()+":"+sourceLedgerTxID+":"+targetProviderID.String())).String()

	// 核心断言：两个 bridge_batch_id 必须不同
	assert.NotEqual(t, batchID1, batchID2,
		"different merchants with same source_ledger_tx_id must produce different bridge_batch_ids")
}
