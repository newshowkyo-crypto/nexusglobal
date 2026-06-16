package listener

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	adapterdomain "github.com/nexusglobal/mamr/internal/adapter/domain"
	"github.com/nexusglobal/mamr/internal/adapter/registry"
	"github.com/nexusglobal/mamr/internal/cryptopay/domain"
	"github.com/nexusglobal/mamr/internal/cryptopay/service"
)

// ChainListener 链上监听器
type ChainListener struct {
	tenantID         uuid.UUID
	channelID        uuid.UUID
	network          domain.NetworkType
	rpcURL           string
	indexerURL       string
	pollInterval     time.Duration
	confirmThreshold int
	service          *service.CryptoPayService
	registry         *registry.UniversalAdapterRegistry
	running          bool
	cancel           context.CancelFunc
}

// NewChainListener 创建链上监听器
func NewChainListener(
	tenantID uuid.UUID,
	channelID uuid.UUID,
	network domain.NetworkType,
	rpcURL string,
	indexerURL string,
	pollInterval time.Duration,
	confirmThreshold int,
	service *service.CryptoPayService,
	registry *registry.UniversalAdapterRegistry,
) *ChainListener {
	return &ChainListener{
		tenantID:         tenantID,
		channelID:        channelID,
		network:          network,
		rpcURL:           rpcURL,
		indexerURL:       indexerURL,
		pollInterval:     pollInterval,
		confirmThreshold: confirmThreshold,
		service:          service,
		registry:         registry,
		running:          false,
	}
}

// Start 启动监听器
func (l *ChainListener) Start(ctx context.Context) error {
	if l.running {
		return fmt.Errorf("listener already running for network %s", l.network)
	}

	l.running = true
	ctx, l.cancel = context.WithCancel(ctx)

	go l.pollingLoop(ctx)

	return nil
}

// Stop 停止监听器
func (l *ChainListener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	l.running = false
}

// IsRunning 检查监听器是否运行中
func (l *ChainListener) IsRunning() bool {
	return l.running
}

// pollingLoop 轮询循环
func (l *ChainListener) pollingLoop(ctx context.Context) {
	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.poll(ctx); err != nil {
				// 记录错误但不停止监听
				fmt.Printf("polling error for network %s: %v\n", l.network, err)
			}
		}
	}
}

// poll 执行轮询
func (l *ChainListener) poll(ctx context.Context) error {
	// 获取适配器 bundle
	bundle, err := l.registry.Get(l.channelID)
	if err != nil {
		return fmt.Errorf("get adapter failed: %w", err)
	}

	// 获取入金适配器
	inboundAdapter, ok := bundle.Client.(adapterdomain.InboundAdapter)
	if !ok {
		return fmt.Errorf("adapter does not support inbound operations")
	}

	// 查询待处理的意图
	pendingIntents, err := l.service.GetPendingDepositIntents(ctx, l.tenantID, 50)
	if err != nil {
		return fmt.Errorf("get pending intents failed: %w", err)
	}

	for _, intent := range pendingIntents {
		// 检查是否已经过期
		if time.Now().After(intent.ExpiresAt) {
			continue
		}

		// 查询意图状态（调用外部适配器）
		queryReq := adapterdomain.QueryInboundIntentRequest{
			ProviderRef: intent.ProviderRef,
		}

		resp, err := inboundAdapter.QueryInboundIntent(ctx, queryReq)
		if err != nil {
			// 如果是未知状态，记录错误但继续处理其他意图
			if errors.Is(err, adapterdomain.ErrUnknownExternalState) {
				continue
			}
			return fmt.Errorf("query intent %s failed: %w", intent.ID, err)
		}

		// 根据响应状态更新意图
		if err := l.updateIntentFromResponse(ctx, intent, resp); err != nil {
			return fmt.Errorf("update intent %s failed: %w", intent.ID, err)
		}
	}

	return nil
}

// updateIntentFromResponse 根据适配器响应更新意图
func (l *ChainListener) updateIntentFromResponse(
	ctx context.Context,
	intent domain.DepositIntent,
	resp *adapterdomain.InboundIntentStatusResponse,
) error {
	// 更新确认数
	if resp.Confirmations > 0 {
		intent.Confirmations = resp.Confirmations
	}

	// 更新状态
	switch resp.Status {
	case adapterdomain.InboundStatusWaitingFunds:
		intent.State = domain.DepositStateWaitingFunds
	case adapterdomain.InboundStatusPartiallyMatched:
		intent.State = domain.DepositStateConfirming
		if !resp.ReceivedAmount.IsZero() {
			intent.ReceivedAmount = resp.ReceivedAmount
		}
	case adapterdomain.InboundStatusConfirmed:
		if intent.Confirmations >= intent.ConfirmationsRequired {
			intent.State = domain.DepositStateConfirmed
			intent.ConfirmedAmount = resp.ConfirmedAmount
			intent.TransactionRef = resp.TransactionRef
		} else {
			intent.State = domain.DepositStateConfirming
		}
	case adapterdomain.InboundStatusReconcileMismatch:
		intent.State = domain.DepositStateReconcileMismatch
	case adapterdomain.InboundStatusFailed:
		intent.State = domain.DepositStateFailed
	}

	return l.service.UpdateDepositIntent(ctx, &intent)
}
