// Package sweepreconcile — SEC-013 修复的 Sweep 对账 Worker。
//
// 消费 Outbox 事件 SWEEP_RECONCILE_REQUESTED：调用 bank.QueryTransfer 核对
// 银行真实状态后，以 CAS 方式写入终态。规则：
//   - bank 状态 completed  → Confirmed（CAS）
//   - bank 状态 failed     → Failed（CAS）
//   - bank 状态 pending/unknown → 返回 error（退避重试；不伪造终态，RED-03）
//
// 处理器幂等：转账已处于终态时直接 ACK。
package sweepreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	treasurydb "github.com/nexusglobal/mamr/internal/treasury/repository/db"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/bank"
)

// Repository 是对账 Worker 所需的最小仓储面（复用 treasury repository 方法）。
type Repository interface {
	GetTransfer(ctx context.Context, id uuid.UUID) (*treasurydb.SweepTransfer, error)
	UpdateTransferStatus(ctx context.Context, id uuid.UUID, status string, version int32) error
}

// Worker 处理 SWEEP_RECONCILE_REQUESTED。
type Worker struct {
	repo   Repository
	bank   bank.Client
	logger *slog.Logger
}

func New(repo Repository, bankCli bank.Client, logger *slog.Logger) *Worker {
	return &Worker{repo: repo, bank: bankCli, logger: logger}
}

// Handle 处理单条对账命令（满足 outbox.Handler 签名）。
func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != outbox.EventSweepReconcileRequested {
		return fmt.Errorf("sweepreconcile: unexpected event type %s", event.EventType)
	}

	var payload struct {
		TransferID string `json:"transfer_id"`
		BankRef    string `json:"bank_ref"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("sweepreconcile: unmarshal: %w", err)
	}
	transferID, err := uuid.Parse(payload.TransferID)
	if err != nil {
		return fmt.Errorf("sweepreconcile: invalid transfer_id: %w", err)
	}

	transfer, err := w.repo.GetTransfer(ctx, transferID)
	if err != nil {
		return fmt.Errorf("sweepreconcile: get transfer: %w", err)
	}
	if transfer == nil {
		return fmt.Errorf("sweepreconcile: transfer %s not found", transferID)
	}

	// 幂等：终态直接 ACK（重复投递无副作用）。
	switch transfer.Status {
	case "Confirmed", "Failed":
		w.logger.Info("sweepreconcile: transfer already terminal, ack",
			"transfer_id", transferID, "status", transfer.Status)
		return nil
	case "Submitted":
	default:
		return fmt.Errorf("sweepreconcile: transfer %s not reconcilable in status %s", transferID, transfer.Status)
	}

	ref := payload.BankRef
	if ref == "" {
		ref = transfer.BankRef
	}
	if ref == "" {
		return errors.New("sweepreconcile: no bank_ref available for reconciliation")
	}

	qCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := w.bank.QueryTransfer(qCtx, ref)
	if err != nil {
		if errors.Is(err, bank.ErrTimeout) {
			// RED-03: 超时 ≠ 失败。保持 Submitted，退避重试。
			w.logger.Warn("sweepreconcile: bank query timeout, will retry", "transfer_id", transferID)
			return errors.New("sweepreconcile: bank query timeout (will retry)")
		}
		return fmt.Errorf("sweepreconcile: bank query: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(status.Status)) {
	case "completed", "succeeded", "success":
		if err := w.repo.UpdateTransferStatus(ctx, transferID, "Confirmed", transfer.Version); err != nil {
			return fmt.Errorf("sweepreconcile: confirm: %w", err)
		}
		w.logger.Info("sweepreconcile: transfer confirmed via bank query",
			"transfer_id", transferID, "bank_ref", maskRef(ref))
		return nil
	case "failed", "rejected", "returned":
		if err := w.repo.UpdateTransferStatus(ctx, transferID, "Failed", transfer.Version); err != nil {
			return fmt.Errorf("sweepreconcile: fail: %w", err)
		}
		w.logger.Warn("sweepreconcile: transfer failed per bank query",
			"transfer_id", transferID, "bank_ref", maskRef(ref))
		return nil
	default:
		// pending/processing/unknown：不确认、不释放（RED-03），退避重试。
		w.logger.Info("sweepreconcile: bank transfer still pending, will retry",
			"transfer_id", transferID, "bank_status", status.Status)
		return fmt.Errorf("sweepreconcile: bank transfer %s still in status %q (will retry)", transferID, status.Status)
	}
}

func maskRef(ref string) string {
	if len(ref) <= 4 {
		return "****"
	}
	return ref[:2] + "****" + ref[len(ref)-2:]
}
