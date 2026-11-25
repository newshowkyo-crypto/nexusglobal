package payout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexusglobal/mamr/internal/payout/domain"
	"github.com/nexusglobal/mamr/internal/payout/repository"
	"github.com/nexusglobal/mamr/internal/payout/repository/db"
	"github.com/nexusglobal/mamr/internal/platform/failclosed"
	"github.com/nexusglobal/mamr/internal/platform/money"
	"github.com/nexusglobal/mamr/internal/platform/postgres"
	"github.com/nexusglobal/mamr/internal/settlement/projection"
	"github.com/nexusglobal/mamr/internal/worker/outbox"
	"github.com/nexusglobal/mamr/pkg/external/lp"

	ledgerdomain "github.com/nexusglobal/mamr/internal/ledger/domain"
	"github.com/nexusglobal/mamr/internal/ledger/fee"
)

type Worker struct {
	repo       *repository.Repository
	ledgerRepo *repository.LedgerRepo
	lpCli      lp.Client
	feeService fee.FeeServiceInterface
	txm        *postgres.TxManager
	pool       *pgxpool.Pool
	logger     *slog.Logger
	// P0-2: failClosedSvc 用于 ExecutionCheck（LP dispatch 前）。
	// 若为 nil，回退到进程内缓存检查（IsSystemLockedDown）。
	failClosedSvc *failclosed.Service
}

func New(repo *repository.Repository, ledgerRepo *repository.LedgerRepo, lpCli lp.Client, feeService fee.FeeServiceInterface, txm *postgres.TxManager, pool *pgxpool.Pool, logger *slog.Logger) *Worker {
	return &Worker{
		repo:       repo,
		ledgerRepo: ledgerRepo,
		lpCli:      lpCli,
		feeService: feeService,
		txm:        txm,
		pool:       pool,
		logger:     logger,
	}
}

// NewWithFailClosed 创建带 Fail-Closed 服务注入的 payout WorkerC worker（P0-2）。
//
// 生产环境应使用此构造函数，确保 LP dispatch 前经过 ExecutionCheck。
// failClosedSvc != nil 时，Handle 会在调用 lpCli.Payout 前调用 ExecutionCheck(ctx)，
// 封锁或 DB 错误时返回 failclosed.ErrSystemLockdown。
func NewWithFailClosed(repo *repository.Repository, ledgerRepo *repository.LedgerRepo, lpCli lp.Client, feeService fee.FeeServiceInterface, txm *postgres.TxManager, pool *pgxpool.Pool, logger *slog.Logger, failClosedSvc *failclosed.Service) *Worker {
	return &Worker{
		repo:          repo,
		ledgerRepo:    ledgerRepo,
		lpCli:         lpCli,
		feeService:    feeService,
		txm:           txm,
		pool:          pool,
		logger:        logger,
		failClosedSvc: failClosedSvc,
	}
}

// SetFailClosedService 注入 Fail-Closed 服务（用于已构造的 Worker）。
func (w *Worker) SetFailClosedService(svc *failclosed.Service) {
	w.failClosedSvc = svc
}

// executionCheck 是 LP dispatch 前的统一封锁检查（P0-2）。
// 若 failClosedSvc 为 nil，回退到进程内缓存检查（向后兼容）。
// 否则调用 ExecutionCheck(ctx)，封锁或 DB 错误时返回 failclosed.ErrSystemLockdown。
func (w *Worker) executionCheck(ctx context.Context) error {
	if w.failClosedSvc != nil {
		return w.failClosedSvc.ExecutionCheck(ctx)
	}
	if failclosed.IsSystemLockedDown() {
		return failclosed.ErrSystemLockdown
	}
	return nil
}

func (w *Worker) Handle(ctx context.Context, event outbox.OutboxEvent) error {
	if event.EventType != "LP_PAYOUT_REQUESTED" {
		return fmt.Errorf("payout worker: unexpected event type %s", event.EventType)
	}

	var payload struct {
		PayoutRequestID string `json:"payout_request_id"`
		LockID          string `json:"lock_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("payout worker: unmarshal: %w", err)
	}

	payoutID, err := uuid.Parse(payload.PayoutRequestID)
	if err != nil {
		return fmt.Errorf("payout worker: invalid id: %w", err)
	}

	payoutReq, err := w.repo.GetPayoutRequest(ctx, payoutID)
	if err != nil {
		return fmt.Errorf("payout worker: get request: %w", err)
	}
	if payoutReq == nil {
		return fmt.Errorf("payout worker: request %s not found", payoutID)
	}

	if payoutReq.Status != string(domain.StatusFxLocked) {
		w.logger.Info("payout worker: skipping non-fxlocked",
			"payout_id", payoutID, "status", payoutReq.Status)
		return nil
	}

	// P0-2: LP dispatch 前强一致封锁检查。
	// ExecutionCheck 查询 DB 持久状态，DB 错误时 fail-closed。
	// 封锁时返回 error，outbox worker 会重试或进入死信。
	if err := w.executionCheck(ctx); err != nil {
		w.logger.Warn("payout worker: blocked by system lockdown, will retry",
			"payout_id", payoutID, "err", err)
		return fmt.Errorf("payout worker: system lockdown: %w", err)
	}

	amt := money.NumericToDecimal(payoutReq.Amount)

	payoutReq2 := lp.PayoutRequest{
		PayoutRequestID: payoutID,
		LockID:          payload.LockID,
		Amount:          amt,
		FiatCurrency:    payoutReq.FiatCurrency,
		TargetAsset:     payoutReq.TargetAsset,
		TargetNetwork:   payoutReq.TargetNetwork,
	}

	payoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := w.lpCli.Payout(payoutCtx, payoutReq2)
	if err != nil {
		if errors.Is(err, lp.ErrTimeout) {
			w.logger.Warn("payout worker: LP timeout",
				"payout_id", payoutID)
			return w.setStatus(ctx, payoutID, string(domain.StatusUnknownExternalState), payoutReq.Version)
		}
		if errors.Is(err, lp.ErrExplicitFailure) {
			w.logger.Warn("payout worker: LP failure, releasing",
				"payout_id", payoutID)
			return w.releaseFrozen(ctx, payoutID, payoutReq)
		}
		return fmt.Errorf("payout worker: lp.Payout: %w", err)
	}

	w.logger.Info("payout worker: submitted",
		"payout_id", payoutID, "provider_ref", result.ProviderRef)

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusLPProcessing), payoutReq.Version); err != nil {
			return fmt.Errorf("update status: %w", err)
		}

		// G2: 持久化 provider_ref
		if err := w.repo.UpdatePayoutRequestProviderRef(ctx, tx, payoutID, result.ProviderRef); err != nil {
			return fmt.Errorf("update provider_ref: %w", err)
		}

		return nil
	})
}

func (w *Worker) releaseFrozen(ctx context.Context, payoutID uuid.UUID, payoutReq *db.PayoutRequest) error {
	err := w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusFailed), payoutReq.Version)
	})
	if err != nil {
		return err
	}
	return w.Release(ctx, payoutID)
}

func (w *Worker) setStatus(ctx context.Context, payoutID uuid.UUID, status string, version int32) error {
	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		return w.repo.UpdatePayoutRequestStatus(ctx, tx, payoutID, status, version)
	})
}

func (w *Worker) Confirm(ctx context.Context, payoutID uuid.UUID, providerRef string) error {
	idempotencyKey := "payout_confirm:" + payoutID.String()

	existing, err := w.ledgerRepo.GetTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return fmt.Errorf("confirm: idempotency check: %w", err)
	}
	if existing != nil {
		w.logger.Info("confirm: already posted (idempotent)", "payout_id", payoutID)
		return nil
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		payoutReq, err := w.repo.LockPayoutRequestForUpdate(ctx, tx, payoutID)
		if err != nil {
			return fmt.Errorf("confirm: lock payout: %w", err)
		}
		if payoutReq == nil {
			return fmt.Errorf("confirm: request %s not found", payoutID)
		}

		if payoutReq.Status == string(domain.StatusConfirmed) || payoutReq.Status == string(domain.StatusReconciled) {
			return nil
		}

		if !domain.CanTransition(domain.PayoutStatus(payoutReq.Status), domain.StatusConfirmed) {
			return fmt.Errorf("confirm: cannot transition from %s to Confirmed", payoutReq.Status)
		}

		amount := money.NumericToDecimal(payoutReq.Amount)
		if amount.IsZero() {
			return fmt.Errorf("confirm: payout amount is zero")
		}

		bal, err := w.repo.LockBalanceForUpdate(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("confirm: lock balance: %w", err)
		}
		if bal == nil {
			return fmt.Errorf("confirm: no treasury ledger for merchant %s currency %s", payoutReq.MerchantID, payoutReq.FiatCurrency)
		}

		// SEC-005: 补齐成对分录（RED-13）。冻结解除 = Dr FROZEN_PAYOUT（负债↓），
		// 对手方 = Cr LP_PAYABLE（平台对 LP 的付款负债↑）。
		frozenAccID, accErr := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "FROZEN_PAYOUT", payoutReq.FiatCurrency)
		if accErr != nil {
			return fmt.Errorf("confirm: get frozen account: %w", accErr)
		}
		lpPayableAccID, accErr := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "LP_PAYABLE", payoutReq.FiatCurrency)
		if accErr != nil {
			return fmt.Errorf("confirm: get lp payable account: %w", accErr)
		}

		_, err = w.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  idempotencyKey,
			TransactionType: ledgerdomain.TransactionType("PAYOUT_CONFIRM"),
			AggregateType:   "payout_request",
			AggregateID:     payoutID.String(),
			Description:     fmt.Sprintf("Payout confirm %s %s", amount.String(), payoutReq.FiatCurrency),
			Amount:          amount,
			UpdateBalances:  true,
			Entries: []ledgerdomain.Entry{
				{AccountID: frozenAccID, Direction: ledgerdomain.DirectionDebit, Amount: amount},
				{AccountID: lpPayableAccID, Direction: ledgerdomain.DirectionCredit, Amount: amount},
			},
		})
		if err != nil {
			return fmt.Errorf("confirm: ledger post: %w", err)
		}

		// P0 4.5：投影唯一写入口（出金成功，冻结清零）
		if err := projection.RemoveFrozenTx(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency,
			money.ToMinor(amount).Int64()); err != nil {
			return fmt.Errorf("confirm: update projection: %w", err)
		}

		if err := w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusConfirmed), payoutReq.Version); err != nil {
			return fmt.Errorf("confirm: update status: %w", err)
		}

		// 记录代付费用（BANK_FEE 和 NETWORK_FEE）
		if w.feeService != nil {
			feeDetails := []fee.FeeDetail{}

			// 模拟从 provider response 或配置中获取费用信息
			// 在实际场景中，这些费用应从 LP 返回的结果中提取
			bankFeeAmount := int64(100)   // 模拟银行费用（最小单位）
			networkFeeAmount := int64(50) // 模拟网络费用（最小单位）

			if bankFeeAmount > 0 {
				feeDetails = append(feeDetails, fee.FeeDetail{
					FeeType:    fee.FeeTypeBank,
					Amount:     bankFeeAmount,
					SourceData: map[string]interface{}{"payout_id": payoutID.String(), "provider_ref": providerRef, "fee_type": "bank_fee"},
				})
			}

			if networkFeeAmount > 0 {
				feeDetails = append(feeDetails, fee.FeeDetail{
					FeeType:    fee.FeeTypeNetwork,
					Amount:     networkFeeAmount,
					SourceData: map[string]interface{}{"payout_id": payoutID.String(), "provider_ref": providerRef, "fee_type": "network_fee", "network": payoutReq.TargetNetwork},
				})
			}

			if len(feeDetails) > 0 {
				go func() {
					if err := w.feeService.AllocateTransactionFees(context.Background(), payoutID, payoutReq.MerchantID, payoutReq.FiatCurrency, feeDetails); err != nil {
						w.logger.Error("failed to allocate payout fees", "payout_id", payoutID, "error", err)
					}
				}()
			}
		}

		return nil
	})
}

func (w *Worker) Release(ctx context.Context, payoutID uuid.UUID) error {
	idempotencyKey := "payout_release:" + payoutID.String()

	existing, err := w.ledgerRepo.GetTransactionByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return fmt.Errorf("release: idempotency check: %w", err)
	}
	if existing != nil {
		w.logger.Info("release: already posted (idempotent)", "payout_id", payoutID)
		return nil
	}

	return w.txm.WithinTx(ctx, func(tx pgx.Tx) error {
		payoutReq, err := w.repo.LockPayoutRequestForUpdate(ctx, tx, payoutID)
		if err != nil {
			return fmt.Errorf("release: lock payout: %w", err)
		}
		if payoutReq == nil {
			return fmt.Errorf("release: request %s not found", payoutID)
		}

		if payoutReq.Status == string(domain.StatusBalanceReleased) {
			return nil
		}

		// T5-2 资金红线：人工调单期间 frozen 绝不释放。
		// 必须等人工核销清除 manual_review 标记后才允许走释放/核销分录。
		if payoutReq.ManualReview {
			w.logger.Warn("release: blocked, payout under manual review",
				"payout_id", payoutID, "reason", payoutReq.ManualReviewReason)
			return fmt.Errorf("release: payout %s is under manual review, frozen balance held", payoutID)
		}

		if !domain.CanTransition(domain.PayoutStatus(payoutReq.Status), domain.StatusBalanceReleased) {
			return fmt.Errorf("release: cannot transition from %s to BalanceReleased", payoutReq.Status)
		}

		amount := money.NumericToDecimal(payoutReq.Amount)
		if amount.IsZero() {
			return fmt.Errorf("release: payout amount is zero")
		}

		bal, err := w.repo.LockBalanceForUpdate(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency)
		if err != nil {
			return fmt.Errorf("release: lock balance: %w", err)
		}
		if bal == nil {
			return fmt.Errorf("release: no treasury ledger for merchant %s currency %s", payoutReq.MerchantID, payoutReq.FiatCurrency)
		}

		// SEC-005: 补齐成对分录（RED-13）。释放回结算 = Dr FROZEN_PAYOUT（负债↓），
		// Cr SETTLED（负债↑），与 Freeze 的 Dr SETTLED / Cr FROZEN_PAYOUT 互为逆操作。
		frozenAccID, accErr := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "FROZEN_PAYOUT", payoutReq.FiatCurrency)
		if accErr != nil {
			return fmt.Errorf("release: get frozen account: %w", accErr)
		}
		settledAccID, accErr := w.ledgerRepo.GetOrCreateAccount(ctx, tx, payoutReq.MerchantID, "SETTLED", payoutReq.FiatCurrency)
		if accErr != nil {
			return fmt.Errorf("release: get settled account: %w", accErr)
		}

		_, err = w.ledgerRepo.PostTransaction(ctx, tx, ledgerdomain.Transaction{
			IdempotencyKey:  idempotencyKey,
			TransactionType: ledgerdomain.TransactionType("PAYOUT_RELEASE"),
			AggregateType:   "payout_request",
			AggregateID:     payoutID.String(),
			Description:     fmt.Sprintf("Payout release %s %s", amount.String(), payoutReq.FiatCurrency),
			Amount:          amount,
			UpdateBalances:  true,
			Entries: []ledgerdomain.Entry{
				{AccountID: frozenAccID, Direction: ledgerdomain.DirectionDebit, Amount: amount},
				{AccountID: settledAccID, Direction: ledgerdomain.DirectionCredit, Amount: amount},
			},
		})
		if err != nil {
			return fmt.Errorf("release: ledger post: %w", err)
		}

		// P0 4.5：投影唯一写入口（出金失败，冻结退回可出金）
		if err := projection.MoveFrozenToSettledTx(ctx, tx, payoutReq.MerchantID, payoutReq.FiatCurrency,
			money.ToMinor(amount).Int64()); err != nil {
			return fmt.Errorf("release: update projection: %w", err)
		}

		return w.repo.UpdatePayoutRequestStatus(ctx, tx,
			payoutID, string(domain.StatusBalanceReleased), payoutReq.Version)
	})
}
