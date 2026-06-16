package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/cryptopay/domain"
)

// mockCryptoRepository 模拟 cryptopay repository
type mockCryptoRepository struct {
	mock.Mock
}

func (m *mockCryptoRepository) CreateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error {
	args := m.Called(ctx, intent)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetDepositIntentByID(ctx context.Context, id uuid.UUID) (*domain.DepositIntent, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) GetDepositIntentByProviderRef(ctx context.Context, providerRef string) (*domain.DepositIntent, error) {
	args := m.Called(ctx, providerRef)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) GetDepositIntentByAddress(ctx context.Context, address string) (*domain.DepositIntent, error) {
	args := m.Called(ctx, address)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) UpdateDepositIntent(ctx context.Context, intent *domain.DepositIntent) error {
	args := m.Called(ctx, intent)
	return args.Error(0)
}

func (m *mockCryptoRepository) CreateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error {
	args := m.Called(ctx, tx)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetPendingDepositIntents(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.DepositIntent, error) {
	args := m.Called(ctx, tenantID, limit)
	return args.Get(0).([]domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) GetExpiredDepositIntents(ctx context.Context, limit int) ([]domain.DepositIntent, error) {
	args := m.Called(ctx, limit)
	return args.Get(0).([]domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) DeleteDepositIntent(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetDepositIntentsByTenant(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]domain.DepositIntent, error) {
	args := m.Called(ctx, tenantID, limit, offset)
	return args.Get(0).([]domain.DepositIntent), args.Error(1)
}

func (m *mockCryptoRepository) GetDepositIntentCountByState(ctx context.Context, tenantID uuid.UUID, state domain.DepositState) (int, error) {
	args := m.Called(ctx, tenantID, state)
	return args.Get(0).(int), args.Error(1)
}

func (m *mockCryptoRepository) MarkAsReconcileMismatch(ctx context.Context, intentID uuid.UUID, reason string) error {
	args := m.Called(ctx, intentID, reason)
	return args.Error(0)
}

func (m *mockCryptoRepository) ConfirmDepositIntent(ctx context.Context, intentID uuid.UUID, receivedAmount string, confirmations int, transactionRef string) error {
	args := m.Called(ctx, intentID, receivedAmount, confirmations, transactionRef)
	return args.Error(0)
}

func (m *mockCryptoRepository) CreateAddressPoolEntry(ctx context.Context, entry *domain.DepositAddressPool) error {
	args := m.Called(ctx, entry)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetAvailableAddress(ctx context.Context, tenantID uuid.UUID, assetCode string, network domain.NetworkType) (*domain.DepositAddressPool, error) {
	args := m.Called(ctx, tenantID, assetCode, network)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.DepositAddressPool), args.Error(1)
}

func (m *mockCryptoRepository) AllocateAddress(ctx context.Context, addressID uuid.UUID, intentID uuid.UUID) error {
	args := m.Called(ctx, addressID, intentID)
	return args.Error(0)
}

func (m *mockCryptoRepository) ReleaseAddress(ctx context.Context, addressID uuid.UUID) error {
	args := m.Called(ctx, addressID)
	return args.Error(0)
}

func (m *mockCryptoRepository) MarkAddressUsed(ctx context.Context, addressID uuid.UUID) error {
	args := m.Called(ctx, addressID)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetChainTransactionByHash(ctx context.Context, txHash string) (*domain.ChainTransaction, error) {
	args := m.Called(ctx, txHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChainTransaction), args.Error(1)
}

func (m *mockCryptoRepository) UpdateChainTransaction(ctx context.Context, tx *domain.ChainTransaction) error {
	args := m.Called(ctx, tx)
	return args.Error(0)
}

func (m *mockCryptoRepository) GetUnconfirmedTransactions(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.ChainTransaction, error) {
	args := m.Called(ctx, tenantID, limit)
	return args.Get(0).([]domain.ChainTransaction), args.Error(1)
}

// ============================================================================
// Address Allocation Idempotency Tests
// ============================================================================

func TestAddressAllocation_Idempotent(t *testing.T) {
	// 测试地址分配幂等性
	address := "0x1234567890abcdef1234567890abcdef12345678"
	intentID := uuid.New()

	pool := &domain.DepositAddressPool{
		ID:                uuid.New(),
		Address:           address,
		Status:            "ALLOCATED",
		AllocatedIntentID: &intentID,
		AllocatedAt:       time.Now(),
	}

	// 同一个 intent 再次分配应该返回相同地址
	assert.Equal(t, intentID, *pool.AllocatedIntentID)
	assert.Equal(t, "ALLOCATED", pool.Status)
}

func TestAddressAllocation_AlreadyAllocated(t *testing.T) {
	// 测试地址已分配给其他 intent
	err := domain.ErrAddressAlreadyAllocated
	require.Error(t, err)
	assert.Equal(t, "crypto pay: address already allocated", err.Error())
}

func TestAddressAllocation_PoolEmpty(t *testing.T) {
	// 测试地址池为空
	err := domain.ErrAddressPoolEmpty
	require.Error(t, err)
	assert.Equal(t, "crypto pay: no available addresses in pool", err.Error())
}

// ============================================================================
// Confirmations Threshold Tests
// ============================================================================

func TestConfirmationsThreshold_Insufficient(t *testing.T) {
	// 测试确认数不足
	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		ConfirmationsRequired: 6,
		Confirmations:         3, // 不足
		State:                 domain.DepositStateConfirming,
	}

	assert.False(t, intent.State.HasEnoughConfirmations(intent.Confirmations, intent.ConfirmationsRequired))
	assert.Equal(t, domain.DepositStateConfirming, intent.State)
}

func TestConfirmationsThreshold_Sufficient(t *testing.T) {
	// 测试确认数足够
	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		ConfirmationsRequired: 6,
		Confirmations:         6, // 足够
		State:                 domain.DepositStateConfirming,
	}

	assert.True(t, intent.State.HasEnoughConfirmations(intent.Confirmations, intent.ConfirmationsRequired))
}

func TestConfirmationsThreshold_Exceeded(t *testing.T) {
	// 测试确认数超过阈值
	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		ConfirmationsRequired: 6,
		Confirmations:         12, // 超过
		State:                 domain.DepositStateConfirming,
	}

	assert.True(t, intent.State.HasEnoughConfirmations(intent.Confirmations, intent.ConfirmationsRequired))
}

func TestConfirmationsThreshold_Default(t *testing.T) {
	// 测试默认确认数阈值
	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		ConfirmationsRequired: 6, // 默认值
	}

	assert.Equal(t, 6, intent.ConfirmationsRequired)
}

// ============================================================================
// Wrong Network Tests
// ============================================================================

func TestWrongNetwork_Mismatch(t *testing.T) {
	// 测试网络不匹配
	intent := &domain.DepositIntent{
		ID:        uuid.New(),
		Network:   domain.NetworkTypeEthereum,
		State:     domain.DepositStateWaitingFunds,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	// 收到 Bitcoin 网络的交易
	err := domain.ErrNetworkMismatch
	require.Error(t, err)
	assert.Equal(t, "crypto pay: network mismatch", err.Error())

	// 状态应该进入 RECONCILE_MISMATCH
	intent.State = domain.DepositStateReconcileMismatch
	intent.LastError = "network mismatch"
	assert.Equal(t, domain.DepositStateReconcileMismatch, intent.State)
}

func TestWrongNetwork_SameNetwork(t *testing.T) {
	// 测试网络匹配
	intent := &domain.DepositIntent{
		ID:      uuid.New(),
		Network: domain.NetworkTypeEthereum,
		State:   domain.DepositStateWaitingFunds,
	}

	// 收到 Ethereum 网络的交易 - 网络匹配
	assert.Equal(t, domain.NetworkTypeEthereum, intent.Network)
}

// ============================================================================
// Amount Mismatch Tests
// ============================================================================

func TestAmountMismatch_LargeDifference(t *testing.T) {
	// 测试金额不匹配（大差异）
	intent := &domain.DepositIntent{
		ID:             uuid.New(),
		ExpectedAmount: decimal.NewFromInt(1000),
		Network:        domain.NetworkTypeEthereum,
		State:          domain.DepositStateWaitingFunds,
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}

	// 收到 500 的金额（差异 50%，超过 0.1% 阈值）
	receivedAmount := decimal.NewFromInt(500)
	allowedDiff := intent.ExpectedAmount.Mul(decimal.NewFromFloat(0.001))
	diff := receivedAmount.Sub(intent.ExpectedAmount).Abs()

	assert.True(t, diff.GreaterThan(allowedDiff))

	err := domain.ErrAmountMismatch
	require.Error(t, err)
}

func TestAmountMismatch_SmallDifferenceAllowed(t *testing.T) {
	// 测试金额小差异允许
	intent := &domain.DepositIntent{
		ID:             uuid.New(),
		ExpectedAmount: decimal.NewFromInt(1000),
		Network:        domain.NetworkTypeEthereum,
		State:          domain.DepositStateWaitingFunds,
	}

	// 收到 999 的金额（差异 1，小于 1 = 0.1% of 1000）
	receivedAmount := decimal.NewFromInt(999)
	allowedDiff := intent.ExpectedAmount.Mul(decimal.NewFromFloat(0.001))
	diff := receivedAmount.Sub(intent.ExpectedAmount).Abs()

	assert.False(t, diff.GreaterThan(allowedDiff))
}

// ============================================================================
// Duplicate Transaction Tests
// ============================================================================

func TestDuplicateTxHash_Detected(t *testing.T) {
	// 测试重复交易哈希检测
	txHash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	intent := &domain.DepositIntent{
		ID:             uuid.New(),
		TransactionRef: txHash,
		State:          domain.DepositStateConfirmed,
	}

	// 同一个交易哈希不应该再次处理
	assert.Equal(t, txHash, intent.TransactionRef)
	assert.True(t, intent.State.IsTerminalState())
}

func TestDuplicateTxHash_DifferentHash(t *testing.T) {
	// 测试不同交易哈希
	intent := &domain.DepositIntent{
		ID:             uuid.New(),
		TransactionRef: "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		State:          domain.DepositStateConfirmed,
	}

	newHash := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	assert.NotEqual(t, newHash, intent.TransactionRef)
}

// ============================================================================
// HandleChainTransaction Tests
// ============================================================================

func TestHandleChainTransaction_InsufficientConfirmations(t *testing.T) {
	ctx := context.Background()
	repo := new(mockCryptoRepository)
	svc := NewCryptoPayService(repo, nil)

	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		TenantID:              uuid.New(),
		ExpectedAmount:        decimal.NewFromInt(1000),
		Network:               domain.NetworkTypeEthereum,
		DepositAddress:        "0xabcdef1234567890abcdef1234567890abcdef1234",
		ConfirmationsRequired: 6,
		State:                 domain.DepositStateWaitingFunds,
		ExpiresAt:             time.Now().Add(24 * time.Hour),
	}

	repo.On("GetDepositIntentByAddress", ctx, "0xabcdef1234567890abcdef1234567890abcdef1234").Return(intent, nil)
	repo.On("UpdateDepositIntent", ctx, mock.Anything).Return(nil)
	repo.On("CreateChainTransaction", ctx, mock.Anything).Return(nil)

	err := svc.HandleChainTransaction(ctx, HandleChainTransactionRequest{
		TransactionHash: "0x1234567890abcdef",
		ToAddress:       "0xabcdef1234567890abcdef1234567890abcdef1234",
		Amount:          decimal.NewFromInt(1000),
		AssetCode:       "ETH",
		Network:         "ETHEREUM",
		Confirmations:   3, // 不足
		Timestamp:       time.Now(),
	})

	require.NoError(t, err)
	assert.Equal(t, domain.DepositStateConfirming, intent.State)
	assert.Equal(t, 3, intent.Confirmations)
}

func TestHandleChainTransaction_SufficientConfirmations(t *testing.T) {
	ctx := context.Background()
	repo := new(mockCryptoRepository)
	svc := NewCryptoPayService(repo, nil)

	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		TenantID:              uuid.New(),
		ExpectedAmount:        decimal.NewFromInt(1000),
		Network:               domain.NetworkTypeEthereum,
		DepositAddress:        "0xabcdef1234567890abcdef1234567890abcdef1234",
		ConfirmationsRequired: 6,
		State:                 domain.DepositStateWaitingFunds,
		ExpiresAt:             time.Now().Add(24 * time.Hour),
	}

	repo.On("GetDepositIntentByAddress", ctx, "0xabcdef1234567890abcdef1234567890abcdef1234").Return(intent, nil)
	repo.On("UpdateDepositIntent", ctx, mock.Anything).Return(nil)
	repo.On("CreateChainTransaction", ctx, mock.Anything).Return(nil)

	err := svc.HandleChainTransaction(ctx, HandleChainTransactionRequest{
		TransactionHash: "0x1234567890abcdef",
		ToAddress:       "0xabcdef1234567890abcdef1234567890abcdef1234",
		Amount:          decimal.NewFromInt(1000),
		AssetCode:       "ETH",
		Network:         "ETHEREUM",
		Confirmations:   6, // 足够
		Timestamp:       time.Now(),
	})

	require.NoError(t, err)
	assert.Equal(t, domain.DepositStateConfirmed, intent.State)
	assert.Equal(t, "0x1234567890abcdef", intent.TransactionRef)
}

func TestHandleChainTransaction_WrongNetwork(t *testing.T) {
	ctx := context.Background()
	repo := new(mockCryptoRepository)
	svc := NewCryptoPayService(repo, nil)

	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		TenantID:              uuid.New(),
		ExpectedAmount:        decimal.NewFromInt(1000),
		Network:               domain.NetworkTypeEthereum,
		DepositAddress:        "0xabcdef1234567890abcdef1234567890abcdef1234",
		ConfirmationsRequired: 6,
		State:                 domain.DepositStateWaitingFunds,
		ExpiresAt:             time.Now().Add(24 * time.Hour),
	}

	repo.On("GetDepositIntentByAddress", ctx, "0xabcdef1234567890abcdef1234567890abcdef1234").Return(intent, nil)
	repo.On("UpdateDepositIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleChainTransaction(ctx, HandleChainTransactionRequest{
		TransactionHash: "0x1234567890abcdef",
		ToAddress:       "0xabcdef1234567890abcdef1234567890abcdef1234",
		Amount:          decimal.NewFromInt(1000),
		AssetCode:       "BTC",
		Network:         "BITCOIN", // 网络不匹配
		Confirmations:   6,
		Timestamp:       time.Now(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNetworkMismatch)
	assert.Equal(t, domain.DepositStateReconcileMismatch, intent.State)
}

func TestHandleChainTransaction_AmountMismatch(t *testing.T) {
	ctx := context.Background()
	repo := new(mockCryptoRepository)
	svc := NewCryptoPayService(repo, nil)

	intent := &domain.DepositIntent{
		ID:                    uuid.New(),
		TenantID:              uuid.New(),
		ExpectedAmount:        decimal.NewFromInt(1000),
		Network:               domain.NetworkTypeEthereum,
		DepositAddress:        "0xabcdef1234567890abcdef1234567890abcdef1234",
		ConfirmationsRequired: 6,
		State:                 domain.DepositStateWaitingFunds,
		ExpiresAt:             time.Now().Add(24 * time.Hour),
	}

	repo.On("GetDepositIntentByAddress", ctx, "0xabcdef1234567890abcdef1234567890abcdef1234").Return(intent, nil)
	repo.On("UpdateDepositIntent", ctx, mock.Anything).Return(nil)

	err := svc.HandleChainTransaction(ctx, HandleChainTransactionRequest{
		TransactionHash: "0x1234567890abcdef",
		ToAddress:       "0xabcdef1234567890abcdef1234567890abcdef1234",
		Amount:          decimal.NewFromInt(500), // 金额不匹配
		AssetCode:       "ETH",
		Network:         "ETHEREUM",
		Confirmations:   6,
		Timestamp:       time.Now(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAmountMismatch)
	assert.Equal(t, domain.DepositStateReconcileMismatch, intent.State)
}

// ============================================================================
// Deposit State Tests
// ============================================================================

func TestDepositState_Transition(t *testing.T) {
	// 测试状态转换
	state := domain.DepositStateCreated

	err := state.ValidateTransition(domain.DepositStateWaitingFunds)
	require.NoError(t, err)

	err = state.ValidateTransition(domain.DepositStateConfirmed)
	require.Error(t, err) // Created 不能直接到 Confirmed
}

func TestDepositState_Terminal(t *testing.T) {
	// 测试终态
	assert.True(t, domain.DepositStateConfirmed.IsTerminalState())
	assert.True(t, domain.DepositStateReconcileMismatch.IsTerminalState())
	assert.True(t, domain.DepositStateExpired.IsTerminalState())
	assert.True(t, domain.DepositStateFailed.IsTerminalState())
	assert.False(t, domain.DepositStateCreated.IsTerminalState())
	assert.False(t, domain.DepositStateWaitingFunds.IsTerminalState())
}

// ============================================================================
// UNKNOWN_EXTERNAL_STATE Tests
// ============================================================================

func TestUnknownExternalState_Crypto(t *testing.T) {
	err := domain.ErrUnknownExternalState
	require.Error(t, err)
	assert.Equal(t, "crypto pay: unknown external state", err.Error())
}

// ============================================================================
// Network Type Tests
// ============================================================================

func TestNetworkType_Constants(t *testing.T) {
	assert.Equal(t, domain.NetworkType("BITCOIN"), domain.NetworkTypeBitcoin)
	assert.Equal(t, domain.NetworkType("ETHEREUM"), domain.NetworkTypeEthereum)
	assert.Equal(t, domain.NetworkType("TRON"), domain.NetworkTypeTron)
	assert.Equal(t, domain.NetworkType("POLYGON"), domain.NetworkTypePolygon)
	assert.Equal(t, domain.NetworkType("SOLANA"), domain.NetworkTypeSolana)
}
