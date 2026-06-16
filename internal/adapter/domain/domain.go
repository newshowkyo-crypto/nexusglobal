package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// AdapterType 适配器类型
// 所有入金/出金通道都统一为 Adapter
type AdapterType string

const (
	AdapterTypeCardAcquirer   AdapterType = "CARD_ACQUIRER"
	AdapterTypeBankCollection AdapterType = "BANK_COLLECTION"
	AdapterTypeCryptoPay      AdapterType = "CRYPTO_PAY"
	AdapterTypeBankRail       AdapterType = "BANK_RAIL"
	AdapterTypeExchange       AdapterType = "EXCHANGE"
	AdapterTypeLP             AdapterType = "LP"
	AdapterTypeOnchainWallet  AdapterType = "ONCHAIN_WALLET"
)

// AssetType 资产类型
type AssetType string

const (
	AssetTypeFiat       AssetType = "FIAT"
	AssetTypeCrypto     AssetType = "CRYPTO"
	AssetTypeStablecoin AssetType = "STABLECOIN"
)

// AdapterStatus 适配器状态
type AdapterStatus string

const (
	AdapterStatusActive   AdapterStatus = "ACTIVE"
	AdapterStatusDisabled AdapterStatus = "DISABLED"
	AdapterStatusRiskHold AdapterStatus = "RISK_HOLD"
)

// ChannelStrategyMode 通道风控策略模式
type ChannelStrategyMode string

const (
	ChannelStrategyModeDefault      ChannelStrategyMode = "DEFAULT"       // 标准阈值
	ChannelStrategyModeStrict       ChannelStrategyMode = "STRICT"        // 更严格阈值
	ChannelStrategyModeManualReview ChannelStrategyMode = "MANUAL_REVIEW" // 高风险订单转人工
)

// CredentialType 凭证类型（通用化）
type CredentialType string

const (
	CredentialTypeAPIKey         CredentialType = "API_KEY"
	CredentialTypeAPISecret      CredentialType = "API_SECRET"
	CredentialTypeWebhookSecret  CredentialType = "WEBHOOK_SECRET"
	CredentialTypeClientCert     CredentialType = "CLIENT_CERT"
	CredentialTypeSigningKeyRef  CredentialType = "SIGNING_KEY_REF"
	CredentialTypeRPCToken       CredentialType = "RPC_TOKEN"
	CredentialTypeWithdrawKeyRef CredentialType = "WITHDRAW_KEY_REF"
)

// ============================================================================
// 核心错误定义
// ============================================================================

var (
	// ErrAdapterNotFound 适配器未找到
	ErrAdapterNotFound = errors.New("adapter: adapter not found")

	// ErrAdapterDisabled 适配器已禁用
	ErrAdapterDisabled = errors.New("adapter: adapter disabled")

	// ErrAdapterRiskHold 适配器风控冻结
	ErrAdapterRiskHold = errors.New("adapter: adapter in risk hold")

	// ErrUnsupportedAdapterType 不支持的适配器类型
	ErrUnsupportedAdapterType = errors.New("adapter: unsupported adapter type")

	// ErrCredentialMissing 凭证缺失
	ErrCredentialMissing = errors.New("adapter: credential missing")

	// ErrCredentialDecrypt 凭证解密失败
	ErrCredentialDecrypt = errors.New("adapter: credential decrypt failed")

	// ErrAssetNotSupported 资产不支持
	ErrAssetNotSupported = errors.New("adapter: asset not supported")

	// ErrNetworkNotSupported 网络不支持
	ErrNetworkNotSupported = errors.New("adapter: network not supported")

	// ErrUnknownExternalState 未知外部状态
	ErrUnknownExternalState = errors.New("adapter: unknown external state")

	// ErrApprovalRequired 需要审批
	ErrApprovalRequired = errors.New("adapter: approval required")

	// ErrSlippageExceeded 滑点超限
	ErrSlippageExceeded = errors.New("adapter: slippage exceeded max allowed")

	// ErrQuoteExpired 报价过期
	ErrQuoteExpired = errors.New("adapter: quote expired")

	// ErrLiquidityInsufficient 流动性不足
	ErrLiquidityInsufficient = errors.New("adapter: liquidity insufficient")

	// ErrAddressNotAllowlisted 地址不在白名单
	ErrAddressNotAllowlisted = errors.New("adapter: address not allowlisted")

	// ErrLimitExceeded 超过限额
	ErrLimitExceeded = errors.New("adapter: limit exceeded")

	// ErrWebhookSignatureInvalid Webhook 签名无效
	ErrWebhookSignatureInvalid = errors.New("adapter: webhook signature invalid")
)

// ============================================================================
// 资产定义
// ============================================================================

// AssetDefinition 资产定义
type AssetDefinition struct {
	ID              uuid.UUID
	AssetCode       string    // USD, EUR, USDT, BTC
	AssetType       AssetType // FIAT, CRYPTO, STABLECOIN
	Network         string    // TRON, ETHEREUM, BITCOIN (crypto only)
	Decimals        int
	CanonicalSymbol string
	IsActive        bool
	Metadata        map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ============================================================================
// 适配器提供商
// ============================================================================

// AdapterProvider 适配器提供商
type AdapterProvider struct {
	ID             uuid.UUID
	ProviderCode   string
	ProviderName   string
	AdapterType    AdapterType
	ProviderConfig map[string]any // 非敏感配置
	Status         AdapterStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ============================================================================
// 适配器通道
// ============================================================================

// AdapterChannel 适配器通道（通用化）
type AdapterChannel struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID // V3: 法人实体隔离
	MerchantID        *uuid.UUID
	ProviderID        uuid.UUID
	ChannelCode       string
	ChannelName       string
	AdapterType       AdapterType
	SupportedAssets   []string       // 支持的资产代码列表
	SupportedNetworks []string       // 支持的网络列表（crypto）
	ChannelConfig     map[string]any // 非敏感通道配置
	RoutingConfig     map[string]any // 路由配置
	Status            AdapterStatus
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// V3 风控字段
	StrategyMode            ChannelStrategyMode // 风控策略模式
	ChargebackThresholdBps  int                 // 拒付率阈值 (bps)
	RefundThresholdBps      int                 // 退款率阈值 (bps)
	FailureRateThresholdBps int                 // 失败率阈值 (bps)
	PipelineGroupID         *uuid.UUID          // 管道隔离组ID
	RiskOverrideEnabled     bool                // 是否启用风控覆盖
	RiskOverrideReason      *string             // 覆盖原因
	RiskOverrideOperator    *string             // 操作人
	RiskOverrideExpiresAt   *time.Time          // 过期时间
}

// ============================================================================
// 凭证 Vault
// ============================================================================

// AdapterCredentialVault 凭证加密存储
type AdapterCredentialVault struct {
	ID             uuid.UUID
	ProviderID     uuid.UUID
	ChannelID      *uuid.UUID
	CredentialType CredentialType
	KeyVersion     int
	Ciphertext     []byte // AES-256-GCM 加密密文
	Nonce          []byte // GCM nonce
	AADContext     string // AAD 绑定上下文
	ExpiresAt      *time.Time
	Status         string // ACTIVE, REVOKED, EXPIRED
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ============================================================================
// Adapter Bundle（热路径内存对象）
// ============================================================================

// AdapterBundle 适配器捆绑包（原子替换）
// 交易热路径使用，不得访问 PG/KMS
type AdapterBundle struct {
	ChannelID         uuid.UUID
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	ChannelCode       string
	ProviderID        uuid.UUID
	ProviderCode      string
	AdapterType       AdapterType
	Client            AdapterClient
	SupportedAssets   []string
	SupportedNetworks []string
	Config            map[string]any
	RoutingConfig     map[string]any
	Version           int
	IsActive          bool
	LoadedAt          time.Time
	LoadError         error // 加载失败时记录，fail-closed 或 stale-but-serving
}

// ============================================================================
// Contract Interfaces
// ============================================================================

// AdapterClient 通用适配器客户端接口
// 所有适配器必须实现此接口
type AdapterClient interface {
	// GetAdapterType 返回适配器类型
	GetAdapterType() AdapterType

	// SupportsAsset 检查是否支持指定资产
	SupportsAsset(assetCode string) bool

	// SupportsNetwork 检查是否支持指定网络（crypto）
	SupportsNetwork(network string) bool
}

// InboundAdapter 入金适配器接口
// CARD_ACQUIRER, BANK_COLLECTION, CRYPTO_PAY 必须实现
type InboundAdapter interface {
	AdapterClient

	// CreateInboundIntent 创建入金意图
	CreateInboundIntent(ctx context.Context, req InboundIntentRequest) (*InboundIntentResponse, error)

	// QueryInboundIntent 查询入金意图状态
	QueryInboundIntent(ctx context.Context, req QueryInboundIntentRequest) (*InboundIntentStatusResponse, error)

	// VerifyInboundWebhook 验证入金 webhook
	VerifyInboundWebhook(ctx context.Context, payload []byte, signature string) (*InboundWebhookEvent, error)
}

// BridgeAdapter 资产桥适配器接口
// BANK_RAIL, EXCHANGE, LP, ONCHAIN_WALLET 必须实现
type BridgeAdapter interface {
	AdapterClient

	// CreateTransfer 创建转账
	CreateTransfer(ctx context.Context, req TransferRequest) (*TransferResponse, error)

	// QueryTransfer 查询转账状态
	QueryTransfer(ctx context.Context, req QueryTransferRequest) (*TransferStatus, error)

	// CancelTransfer 取消转账（如果支持）
	CancelTransfer(ctx context.Context, req CancelTransferRequest) error
}

// ExchangeAdapter 交易所适配器接口
// EXCHANGE 必须实现
type ExchangeAdapter interface {
	BridgeAdapter

	// GetOrderBook 获取订单簿
	GetOrderBook(ctx context.Context, req OrderBookRequest) (*OrderBookResponse, error)

	// PlaceLimitOrder 下限价单
	PlaceLimitOrder(ctx context.Context, req LimitOrderRequest) (*OrderResponse, error)

	// PlaceMarketOrder 下市价单（默认禁止，需显式配置允许）
	PlaceMarketOrder(ctx context.Context, req MarketOrderRequest) (*OrderResponse, error)

	// QueryOrder 查询订单状态
	QueryOrder(ctx context.Context, req QueryOrderRequest) (*OrderStatus, error)

	// CancelOrder 取消订单
	CancelOrder(ctx context.Context, req CancelOrderRequest) error

	// Withdraw 提现
	Withdraw(ctx context.Context, req WithdrawRequest) (*WithdrawResponse, error)
}

// LPAdapter 流动性提供商适配器接口
// LP 必须实现
type LPAdapter interface {
	BridgeAdapter

	// GetQuote 获取报价
	GetQuote(ctx context.Context, req QuoteRequest) (*QuoteResponse, error)

	// AcceptQuote 接受报价
	AcceptQuote(ctx context.Context, req AcceptQuoteRequest) (*QuoteAcceptResponse, error)

	// QueryQuote 查询报价状态
	QueryQuote(ctx context.Context, req QueryQuoteRequest) (*QuoteStatus, error)
}

// ============================================================================
// 入金请求/响应结构
// ============================================================================

// InboundIntentRequest 入金意图请求
type InboundIntentRequest struct {
	TenantID           uuid.UUID
	CorporateEntityID  *uuid.UUID
	MerchantID         *uuid.UUID
	OrderID            string
	AssetCode          string
	Network            string // crypto only
	Amount             decimal.Decimal
	Currency           string // fiat only
	PaymentMethod      string // card only
	PaymentMethodToken string // card only (real mode required)
	BuyerRef           string // bank collection reference
	MemoTag            string // crypto memo/tag
	IdempotencyKey     string
	Metadata           map[string]any
}

// InboundIntentResponse 入金意图响应
type InboundIntentResponse struct {
	IntentID       uuid.UUID
	ProviderRef    string
	Status         InboundIntentStatus
	VirtualAccount *VirtualAccountDetails // bank collection
	DepositAddress *DepositAddressDetails // crypto pay
	RedirectURL    string                 // card 3DS
	ExpiresAt      *time.Time
	CreatedAt      time.Time
}

// InboundIntentStatus 入金意图状态
type InboundIntentStatus string

const (
	InboundStatusCreated           InboundIntentStatus = "CREATED"
	InboundStatusWaitingFunds      InboundIntentStatus = "WAITING_FUNDS"
	InboundStatusPartiallyMatched  InboundIntentStatus = "PARTIALLY_MATCHED"
	InboundStatusConfirmed         InboundIntentStatus = "CONFIRMED"
	InboundStatusReconcileMismatch InboundIntentStatus = "RECONCILE_MISMATCH"
	InboundStatusExpired           InboundIntentStatus = "EXPIRED"
	InboundStatusFailed            InboundIntentStatus = "FAILED"
	InboundStatusRequiresAction    InboundIntentStatus = "REQUIRES_ACTION"
)

// VirtualAccountDetails 虚拟账户详情
type VirtualAccountDetails struct {
	AccountNumber    string
	RoutingNumber    string // ACH
	IBAN             string // SEPA
	BankName         string
	BankAddress      string
	PaymentReference string // 唯一引用
}

// DepositAddressDetails 存款地址详情
type DepositAddressDetails struct {
	Address               string
	MemoTag               string
	Network               string
	ConfirmationsRequired int
}

// QueryInboundIntentRequest 查询入金意图请求
type QueryInboundIntentRequest struct {
	IntentID    uuid.UUID
	ProviderRef string
}

// InboundIntentStatusResponse 查询入金意图状态响应
type InboundIntentStatusResponse struct {
	IntentID        uuid.UUID
	ProviderRef     string
	Status          InboundIntentStatus
	ReceivedAmount  decimal.Decimal
	ConfirmedAmount decimal.Decimal
	Confirmations   int // crypto
	TransactionRef  string
	UpdatedAt       time.Time
}

// InboundWebhookEvent 入金 webhook 事件
type InboundWebhookEvent struct {
	EventID        string
	EventType      string
	IntentID       uuid.UUID
	ProviderRef    string
	Amount         decimal.Decimal
	AssetCode      string
	Status         InboundIntentStatus
	TransactionRef string
	Timestamp      time.Time
	RawPayload     []byte
}

// ============================================================================
// 转账请求/响应结构
// ============================================================================

// TransferRequest 转账请求
type TransferRequest struct {
	TenantID          uuid.UUID
	CorporateEntityID *uuid.UUID
	JobID             uuid.UUID
	SourceAsset       string
	TargetAsset       string
	Amount            decimal.Decimal
	Destination       string
	Network           string
	IdempotencyKey    string
	Metadata          map[string]any
}

// TransferResponse 转账响应
type TransferResponse struct {
	TransferID    uuid.UUID
	ProviderRef   string
	Status        TransferStatusType
	EstimatedTime *time.Duration
	CreatedAt     time.Time
}

// TransferStatusType 转账状态
type TransferStatusType string

const (
	TransferStatusPending    TransferStatusType = "PENDING"
	TransferStatusSubmitted  TransferStatusType = "SUBMITTED"
	TransferStatusProcessing TransferStatusType = "PROCESSING"
	TransferStatusCompleted  TransferStatusType = "COMPLETED"
	TransferStatusFailed     TransferStatusType = "FAILED"
	TransferStatusCancelled  TransferStatusType = "CANCELLED"
	TransferStatusUnknown    TransferStatusType = "UNKNOWN_EXTERNAL_STATE"
)

// QueryTransferRequest 查询转账请求
type QueryTransferRequest struct {
	TransferID  uuid.UUID
	ProviderRef string
}

// TransferStatus 转账状态响应
type TransferStatus struct {
	TransferID     uuid.UUID
	ProviderRef    string
	Status         TransferStatusType
	Amount         decimal.Decimal
	Fee            decimal.Decimal
	TransactionRef string
	Confirmations  int
	UpdatedAt      time.Time
}

// CancelTransferRequest 取消转账请求
type CancelTransferRequest struct {
	TransferID  uuid.UUID
	ProviderRef string
	Reason      string
}

// ============================================================================
// Exchange 相关结构
// ============================================================================

// OrderBookRequest 订单簿请求
type OrderBookRequest struct {
	Symbol string // USDUSDT
	Depth  int    // 深度
}

// OrderBookResponse 订单簿响应
type OrderBookResponse struct {
	Symbol    string
	Bids      []OrderBookEntry // 买单
	Asks      []OrderBookEntry // 卖单
	Timestamp time.Time
}

// OrderBookEntry 订单簿条目
type OrderBookEntry struct {
	Price  decimal.Decimal
	Amount decimal.Decimal
}

// LimitOrderRequest 限价单请求
type LimitOrderRequest struct {
	Symbol         string
	Side           OrderSide // BUY, SELL
	Amount         decimal.Decimal
	Price          decimal.Decimal
	TimeInForce    string // GTC, IOC, FOK
	IdempotencyKey string
}

// OrderSide 订单方向
type OrderSide string

const (
	OrderSideBuy  OrderSide = "BUY"
	OrderSideSell OrderSide = "SELL"
)

// MarketOrderRequest 市价单请求
type MarketOrderRequest struct {
	Symbol         string
	Side           OrderSide
	Amount         decimal.Decimal
	IdempotencyKey string
}

// OrderResponse 订单响应
type OrderResponse struct {
	OrderID      string
	ProviderRef  string
	Symbol       string
	Side         OrderSide
	Type         string // LIMIT, MARKET
	Amount       decimal.Decimal
	Price        decimal.Decimal
	FilledAmount decimal.Decimal
	FilledPrice  decimal.Decimal
	Status       OrderStatusType
	CreatedAt    time.Time
}

// OrderStatusType 订单状态
type OrderStatusType string

const (
	OrderStatusNew             OrderStatusType = "NEW"
	OrderStatusPartiallyFilled OrderStatusType = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatusType = "FILLED"
	OrderStatusCancelled       OrderStatusType = "CANCELLED"
	OrderStatusRejected        OrderStatusType = "REJECTED"
	OrderStatusExpired         OrderStatusType = "EXPIRED"
)

// QueryOrderRequest 查询订单请求
type QueryOrderRequest struct {
	OrderID     string
	ProviderRef string
}

// OrderStatus 订单状态响应
type OrderStatus struct {
	OrderID      string
	ProviderRef  string
	Symbol       string
	Side         OrderSide
	Amount       decimal.Decimal
	FilledAmount decimal.Decimal
	FilledPrice  decimal.Decimal
	Status       OrderStatusType
	Fee          decimal.Decimal
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CancelOrderRequest 取消订单请求
type CancelOrderRequest struct {
	OrderID     string
	ProviderRef string
}

// WithdrawRequest 提现请求
type WithdrawRequest struct {
	AssetCode          string
	Network            string
	Amount             decimal.Decimal
	DestinationAddress string
	MemoTag            string
	IdempotencyKey     string
}

// WithdrawResponse 提现响应
type WithdrawResponse struct {
	WithdrawID    string
	ProviderRef   string
	Status        TransferStatusType
	TransactionID string
	CreatedAt     time.Time
}

// ============================================================================
// LP 相关结构
// ============================================================================

// QuoteRequest 报价请求
type QuoteRequest struct {
	SourceAsset string
	TargetAsset string
	Amount      decimal.Decimal
	Direction   string // BUY, SELL
}

// QuoteResponse 报价响应
type QuoteResponse struct {
	QuoteID      string
	SourceAsset  string
	TargetAsset  string
	SourceAmount decimal.Decimal
	TargetAmount decimal.Decimal
	Rate         decimal.Decimal
	SpreadBps    int
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// AcceptQuoteRequest 接受报价请求
type AcceptQuoteRequest struct {
	QuoteID        string
	IdempotencyKey string
}

// QuoteAcceptResponse 接受报价响应
type QuoteAcceptResponse struct {
	QuoteID       string
	ProviderRef   string
	Status        string
	TransactionID string
	CreatedAt     time.Time
}

// QueryQuoteRequest 查询报价请求
type QueryQuoteRequest struct {
	QuoteID     string
	ProviderRef string
}

// QuoteStatus 报价状态响应
type QuoteStatus struct {
	QuoteID       string
	ProviderRef   string
	SourceAsset   string
	TargetAsset   string
	SourceAmount  decimal.Decimal
	TargetAmount  decimal.Decimal
	Status        string
	TransactionID string
	UpdatedAt     time.Time
}

// ============================================================================
// 辅助函数
// ============================================================================

// IsUnknownExternalState 判断是否为未知外部状态
func IsUnknownExternalState(err error) bool {
	return errors.Is(err, ErrUnknownExternalState)
}

// IsApprovalRequired 判断是否需要审批
func IsApprovalRequired(err error) bool {
	return errors.Is(err, ErrApprovalRequired)
}

// IsSlippageExceeded 判断是否滑点超限
func IsSlippageExceeded(err error) bool {
	return errors.Is(err, ErrSlippageExceeded)
}

// IsAdapterAvailable 判断适配器是否可用
func IsAdapterAvailable(bundle *AdapterBundle) bool {
	if bundle == nil {
		return false
	}
	if !bundle.IsActive {
		return false
	}
	if bundle.LoadError != nil {
		return false
	}
	return true
}
