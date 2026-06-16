package reserve_release

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/merchant/riskprofile"
)

// Worker 准备金释放工作器
type Worker struct {
	riskProfileSvc *riskprofile.Service
	logger         *slog.Logger
	pollInterval   time.Duration
}

// NewWorker 创建工作器实例
func NewWorker(riskProfileSvc *riskprofile.Service, logger *slog.Logger, pollInterval time.Duration) *Worker {
	return &Worker{
		riskProfileSvc: riskProfileSvc,
		logger:         logger,
		pollInterval:   pollInterval,
	}
}

// Start 启动工作器
func (w *Worker) Start(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.logger.Info("reserve release worker started", "poll_interval", w.pollInterval)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("reserve release worker stopping")
			return nil
		case <-ticker.C:
			if err := w.processReserves(ctx); err != nil {
				w.logger.Error("failed to process reserves", "error", err)
			}
		}
	}
}

// processReserves 处理到期准备金释放
func (w *Worker) processReserves(ctx context.Context) error {
	entries, err := w.riskProfileSvc.GetReadyToRelease(ctx)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		w.logger.Debug("no reserves ready for release")
		return nil
	}

	w.logger.Info("processing ready reserves", "count", len(entries))

	for _, entry := range entries {
		// 检查商户是否处于风险冻结状态
		isRiskHold, err := w.checkMerchantRiskHold(ctx, entry.MerchantID)
		if err != nil {
			w.logger.Error("failed to check merchant risk hold", "merchant_id", entry.MerchantID, "error", err)
			continue
		}

		if isRiskHold {
			w.logger.Info("merchant is in risk hold, skipping release", "merchant_id", entry.MerchantID, "reserve_id", entry.ID)
			continue
		}

		if err := w.riskProfileSvc.ReleaseReserve(ctx, entry.ID, isRiskHold); err != nil {
			w.logger.Error("failed to release reserve", "reserve_id", entry.ID, "error", err)
			continue
		}

		w.logger.Info("reserve released", "reserve_id", entry.ID, "amount", entry.Amount, "merchant_id", entry.MerchantID)
	}

	return nil
}

// checkMerchantRiskHold 检查商户是否处于风险冻结状态
func (w *Worker) checkMerchantRiskHold(ctx context.Context, merchantID interface{}) (bool, error) {
	// 将 merchantID 转换为 uuid.UUID
	var merchantUUID uuid.UUID
	switch v := merchantID.(type) {
	case uuid.UUID:
		merchantUUID = v
	case string:
		merchantUUID, _ = uuid.Parse(v)
	default:
		w.logger.Error("invalid merchant_id type", "merchant_id", merchantID)
		return false, nil
	}

	// 获取商户风险配置
	profile, err := w.riskProfileSvc.GetRiskProfile(ctx, merchantUUID)
	if err != nil {
		// 如果找不到配置，默认不冻结
		w.logger.Debug("merchant risk profile not found, assuming not in risk hold", "merchant_id", merchantUUID)
		return false, nil
	}

	// 检查是否处于风险冻结状态
	return profile.IsInRiskHold(), nil
}
