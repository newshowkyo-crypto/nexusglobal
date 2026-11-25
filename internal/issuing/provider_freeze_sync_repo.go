package issuing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderFreezeSyncStatus 表示 Provider 冻结同步状态机的状态。
type ProviderFreezeSyncStatus string

const (
	FreezeSyncPending              ProviderFreezeSyncStatus = "PENDING"
	FreezeSyncProcessing           ProviderFreezeSyncStatus = "PROCESSING"
	FreezeSyncSucceeded            ProviderFreezeSyncStatus = "SUCCEEDED"
	FreezeSyncFailed               ProviderFreezeSyncStatus = "FAILED"
	FreezeSyncUnknownExternalState ProviderFreezeSyncStatus = "UNKNOWN_EXTERNAL_STATE"
)

// ProviderFreezeSyncRecord 是 card_provider_freeze_syncs 表的 Go 映射。
type ProviderFreezeSyncRecord struct {
	ID                 uuid.UUID
	EventID            string
	IdempotencyKey     string
	CardID             uuid.UUID
	ProviderID         uuid.UUID
	ProviderCardRef    string
	ProviderRequestKey string
	Status             ProviderFreezeSyncStatus
	Attempts           int
	LastError          string
	LockedBy           string
	LockedAt           *time.Time
	ClaimToken         uuid.UUID // 000050: fencing token
	LeaseVersion       int64     // 000050: 乐观并发版本
	CompletedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ProviderFreezeSyncRepository 是 Provider 冻结同步状态机的持久化接口。
//
// 所有方法必须在短事务内完成，外部 Provider 调用发生在事务外。
//
// P0-7 第二轮 P0-2: fencing token 保护。
// Mark 方法接收 claimToken 和 leaseVersion，SQL WHERE 包含 fencing 条件，
// 旧 Worker 在租约失效后无法覆盖新 Worker 状态。
type ProviderFreezeSyncRepository interface {
	// ClaimOrGetSucceeded 原子地认领或返回已完成的同步任务。
	// - 若 event_id 已存在且 status=SUCCEEDED：校验 payload 一致后返回 (record, true, nil)。
	// - 若 event_id 已存在且 status=PROCESSING 且租约未过期：返回 (record, false, ErrSyncLeaseHeld)。
	// - 若 event_id 已存在且 status=FAILED/UNKNOWN_EXTERNAL_STATE：复用同一 provider_request_key 重新认领。
	// - 若 event_id 不存在：插入新记录并认领。
	// - 若 event_id 已存在但 payload 不一致：返回 (_, false, ErrSyncPayloadMismatch)。
	// 每次认领生成新 claim_token、lease_version+1。
	// 返回 (record, alreadySucceeded, error)
	ClaimOrGetSucceeded(ctx context.Context, eventID, idempotencyKey string, cardID, providerID uuid.UUID, providerCardRef, providerRequestKey, lockedBy string, leaseTTL time.Duration) (*ProviderFreezeSyncRecord, bool, error)

	// MarkSucceeded 标记同步成功（带 fencing 条件）。
	// SQL WHERE 包含 claim_token 和 lease_version，旧 Worker 无法覆盖。
	// 若 RowsAffected == 0，返回 ErrSyncLeaseLost。
	MarkSucceeded(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64) error

	// MarkFailed 标记同步失败（带 fencing 条件）。
	MarkFailed(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error

	// MarkUnknownExternalState 标记同步超时/不确定（带 fencing 条件）。
	MarkUnknownExternalState(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error

	// GetByEventID 按 event_id 查询记录。
	GetByEventID(ctx context.Context, eventID string) (*ProviderFreezeSyncRecord, error)
}

// 同步状态机错误
var (
	ErrSyncLeaseHeld        = errors.New("issuing: sync lease held by another worker")
	ErrSyncAlreadySucceeded = errors.New("issuing: sync already succeeded")
	// R19 P0-4: FAILED/UNKNOWN_EXTERNAL_STATE 状态可重新认领。
	ErrSyncReclaimable = errors.New("issuing: sync record in reclaimable state")
)

// freezeSyncDB 是 dbProviderFreezeSyncRepository 依赖的窄 DB 接口。
// R21 Step F: 便于测试注入 QueryRow/Scan 错误，验证 DB 实现的错误处理。
type freezeSyncDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// dbProviderFreezeSyncRepository 是 ProviderFreezeSyncRepository 的 DB 实现。
type dbProviderFreezeSyncRepository struct {
	pool freezeSyncDB
}

// NewDBProviderFreezeSyncRepository 创建 DB 实现的 Provider 冻结同步仓库。
func NewDBProviderFreezeSyncRepository(pool *pgxpool.Pool) ProviderFreezeSyncRepository {
	return &dbProviderFreezeSyncRepository{pool: pool}
}

func (r *dbProviderFreezeSyncRepository) ClaimOrGetSucceeded(
	ctx context.Context,
	eventID, idempotencyKey string,
	cardID, providerID uuid.UUID,
	providerCardRef, providerRequestKey, lockedBy string,
	leaseTTL time.Duration,
) (*ProviderFreezeSyncRecord, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 先查是否已存在（FOR UPDATE 锁行）
	var rec ProviderFreezeSyncRecord
	var lockedAt *time.Time
	var completedAt *time.Time
	var lastError *string

	err = tx.QueryRow(ctx, `
		SELECT id, event_id, idempotency_key, card_id, provider_id, provider_card_ref,
		       provider_request_key, status, attempts, last_error, locked_by, locked_at,
		       claim_token, lease_version, completed_at, created_at, updated_at
		FROM card_provider_freeze_syncs WHERE event_id = $1 FOR UPDATE
	`, eventID).Scan(
		&rec.ID, &rec.EventID, &rec.IdempotencyKey, &rec.CardID, &rec.ProviderID, &rec.ProviderCardRef,
		&rec.ProviderRequestKey, &rec.Status, &rec.Attempts, &lastError, &rec.LockedBy, &lockedAt,
		&rec.ClaimToken, &rec.LeaseVersion, &completedAt, &rec.CreatedAt, &rec.UpdatedAt,
	)

	if err == nil {
		// 记录已存在
		if lastError != nil {
			rec.LastError = *lastError
		}
		rec.LockedAt = lockedAt
		rec.CompletedAt = completedAt

		// P0-3: 严格校验幂等重放内容
		if rec.IdempotencyKey != idempotencyKey ||
			rec.CardID != cardID ||
			rec.ProviderID != providerID ||
			rec.ProviderCardRef != providerCardRef ||
			rec.ProviderRequestKey != providerRequestKey {
			return nil, false, fmt.Errorf("issuing: event_id %s reused with different payload: %w", eventID, ErrSyncPayloadMismatch)
		}

		// 已成功：直接返回，不再次调用 Provider
		if rec.Status == FreezeSyncSucceeded {
			tx.Rollback(ctx)
			return &rec, true, nil
		}

		// 正在处理且租约未过期：不得重复调用
		// P0-2: 使用 PostgreSQL now() 而非应用时钟
		if rec.Status == FreezeSyncProcessing && lockedAt != nil {
			var leaseValid bool
			err = tx.QueryRow(ctx, `
				SELECT (now() - locked_at) < $2::interval FROM card_provider_freeze_syncs WHERE event_id = $1
			`, eventID, fmt.Sprintf("%d seconds", int(leaseTTL.Seconds()))).Scan(&leaseValid)
			// R20 P0-6: QueryRow/Scan 失败必须立即返回错误，绝不能继续 reclaim
			if err != nil {
				return nil, false, fmt.Errorf("issuing: lease validity query failed: %w", err)
			}
			if leaseValid {
				tx.Rollback(ctx)
				return &rec, false, ErrSyncLeaseHeld
			}
		}

		// FAILED / UNKNOWN_EXTERNAL_STATE / 过期 PROCESSING：重新认领
		// P0-2: 生成新 claim_token、lease_version+1
		// 防止覆盖 SUCCEEDED 终态
		newClaimToken := uuid.New()
		newLeaseVersion := rec.LeaseVersion + 1

		tag, err := tx.Exec(ctx, `
			UPDATE card_provider_freeze_syncs
			SET status = 'PROCESSING', attempts = attempts + 1,
			    locked_by = $2, locked_at = now(),
			    claim_token = $3, lease_version = $4,
			    updated_at = now()
			WHERE event_id = $1 AND status != 'SUCCEEDED'
		`, eventID, lockedBy, newClaimToken, newLeaseVersion)
		if err != nil {
			return nil, false, fmt.Errorf("reclaim sync: %w", err)
		}
		// R18 P0-6: reclaim UPDATE 必须检查 RowsAffected()==1
		if tag.RowsAffected() != 1 {
			return nil, false, fmt.Errorf("issuing: reclaim affected %d rows, expected 1: %w", tag.RowsAffected(), ErrSyncLeaseLost)
		}
		rec.Status = FreezeSyncProcessing
		rec.Attempts++
		rec.LockedBy = lockedBy
		rec.ClaimToken = newClaimToken
		rec.LeaseVersion = newLeaseVersion
		now := time.Now()
		rec.LockedAt = &now

		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit reclaim: %w", err)
		}
		return &rec, false, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("query sync: %w", err)
	}

	// 不存在：插入新记录
	newID := uuid.New()
	newClaimToken := uuid.New()
	now := time.Now()
	_, err = tx.Exec(ctx, `
		INSERT INTO card_provider_freeze_syncs
		    (id, event_id, idempotency_key, card_id, provider_id, provider_card_ref, provider_request_key,
		     status, attempts, locked_by, locked_at, claim_token, lease_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'PROCESSING', 1, $8, now(), $9, 1, now(), now())
	`, newID, eventID, idempotencyKey, cardID, providerID, providerCardRef, providerRequestKey, lockedBy, newClaimToken)
	if err != nil {
		// R19 P0-4: PostgreSQL 23505 不一律返回 ErrSyncLeaseHeld。
		// 冲突后结束失败事务，再按 event_id OR idempotency_key OR provider_request_key 查询所有冲突行。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			tx.Rollback(ctx)

			// R21 P0-6: 按 event_id OR idempotency_key OR provider_request_key 查询所有冲突行
			// 传入完整身份参数，函数内部逐条验证身份一致性
			existingRec, qErr := r.queryExistingByAnyUniqueKey(ctx, eventID, idempotencyKey, providerRequestKey, cardID, providerID, providerCardRef)
			if qErr != nil {
				return nil, false, fmt.Errorf("issuing: concurrent insert and conflict query failed: %w", qErr)
			}

			// R21 P0-6: queryExistingByAnyUniqueKey 已在内部逐条验证身份一致性，
			// 到这里说明所有匹配行身份一致。检查状态。
			if existingRec.Status == FreezeSyncSucceeded {
				return existingRec, true, nil
			}
			// R19 P0-4: 相同身份且 PROCESSING、有效租约：ErrSyncLeaseHeld
			if existingRec.Status == FreezeSyncProcessing {
				return existingRec, false, ErrSyncLeaseHeld
			}
			// R19 P0-4: FAILED/UNKNOWN_EXTERNAL_STATE：进入明确 reclaim 流程
			// 不能永远返回 LeaseHeld，调用方可重试 reclaim
			return existingRec, false, ErrSyncReclaimable
		}
		return nil, false, fmt.Errorf("insert sync: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit insert: %w", err)
	}

	return &ProviderFreezeSyncRecord{
		ID:                 newID,
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
	}, false, nil
}

// MarkSucceeded 标记同步成功（带 fencing 条件）。
// F-09: SQL WHERE 包含 status='PROCESSING' + claim_token + lease_version。
// 终态清除 claim_token、locked_by、locked_at。
func (r *dbProviderFreezeSyncRepository) MarkSucceeded(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE card_provider_freeze_syncs
		SET status = 'SUCCEEDED', completed_at = now(), last_error = NULL,
		    claim_token = NULL, locked_by = NULL, locked_at = NULL,
		    updated_at = now()
		WHERE event_id = $1
		  AND status = 'PROCESSING'
		  AND claim_token = $2
		  AND lease_version = $3
	`, eventID, claimToken, leaseVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("issuing: mark succeeded failed: %w", ErrSyncLeaseLost)
	}
	return nil
}

// MarkFailed 标记同步失败（带 fencing 条件）。
// F-09: SQL WHERE 包含 status='PROCESSING' + claim_token + lease_version。
// 终态清除 claim_token、locked_by、locked_at。
func (r *dbProviderFreezeSyncRepository) MarkFailed(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE card_provider_freeze_syncs
		SET status = 'FAILED', last_error = $4,
		    claim_token = NULL, locked_by = NULL, locked_at = NULL,
		    updated_at = now()
		WHERE event_id = $1
		  AND status = 'PROCESSING'
		  AND claim_token = $2
		  AND lease_version = $3
	`, eventID, claimToken, leaseVersion, lastErr)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("issuing: mark failed failed: %w", ErrSyncLeaseLost)
	}
	return nil
}

// MarkUnknownExternalState 标记同步超时/不确定（带 fencing 条件）。
// F-09: SQL WHERE 包含 status='PROCESSING' + claim_token + lease_version。
// 终态清除 claim_token、locked_by、locked_at。
func (r *dbProviderFreezeSyncRepository) MarkUnknownExternalState(ctx context.Context, eventID string, claimToken uuid.UUID, leaseVersion int64, lastErr string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE card_provider_freeze_syncs
		SET status = 'UNKNOWN_EXTERNAL_STATE', last_error = $4,
		    claim_token = NULL, locked_by = NULL, locked_at = NULL,
		    updated_at = now()
		WHERE event_id = $1
		  AND status = 'PROCESSING'
		  AND claim_token = $2
		  AND lease_version = $3
	`, eventID, claimToken, leaseVersion, lastErr)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("issuing: mark unknown external state failed: %w", ErrSyncLeaseLost)
	}
	return nil
}

func (r *dbProviderFreezeSyncRepository) GetByEventID(ctx context.Context, eventID string) (*ProviderFreezeSyncRecord, error) {
	rec, err := r.queryExistingByEventID(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("sync record not found for event_id %s", eventID)
		}
		return nil, err
	}
	return rec, nil
}

// queryExistingByEventID 按 event_id 查询记录（R18 P0-6: 冲突后重查辅助方法）。
func (r *dbProviderFreezeSyncRepository) queryExistingByEventID(ctx context.Context, eventID string) (*ProviderFreezeSyncRecord, error) {
	var rec ProviderFreezeSyncRecord
	var lockedAt *time.Time
	var completedAt *time.Time
	var lastError *string

	err := r.pool.QueryRow(ctx, `
		SELECT id, event_id, idempotency_key, card_id, provider_id, provider_card_ref,
		       provider_request_key, status, attempts, last_error, locked_by, locked_at,
		       claim_token, lease_version, completed_at, created_at, updated_at
		FROM card_provider_freeze_syncs WHERE event_id = $1
	`, eventID).Scan(
		&rec.ID, &rec.EventID, &rec.IdempotencyKey, &rec.CardID, &rec.ProviderID, &rec.ProviderCardRef,
		&rec.ProviderRequestKey, &rec.Status, &rec.Attempts, &lastError, &rec.LockedBy, &lockedAt,
		&rec.ClaimToken, &rec.LeaseVersion, &completedAt, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastError != nil {
		rec.LastError = *lastError
	}
	rec.LockedAt = lockedAt
	rec.CompletedAt = completedAt
	return &rec, nil
}

// queryExistingByAnyUniqueKey 按 event_id OR idempotency_key OR provider_request_key 查询所有冲突行。
// R21 P0-6: 返回全部记录并在内部逐条验证身份一致性。
// 任一 event/idempotency/provider-request 冲突行身份不一致都返回 ErrSyncPayloadMismatch。
// 优先返回 event_id 匹配的行；否则返回 idempotency_key 或 provider_request_key 匹配的行。
func (r *dbProviderFreezeSyncRepository) queryExistingByAnyUniqueKey(ctx context.Context, eventID, idempotencyKey, providerRequestKey string, cardID, providerID uuid.UUID, providerCardRef string) (*ProviderFreezeSyncRecord, error) {
	// R20 P0-6: 查询所有匹配行，不使用 LIMIT 1
	rows, err := r.pool.Query(ctx, `
		SELECT id, event_id, idempotency_key, card_id, provider_id, provider_card_ref,
		       provider_request_key, status, attempts, last_error, locked_by, locked_at,
		       claim_token, lease_version, completed_at, created_at, updated_at
		FROM card_provider_freeze_syncs
		WHERE event_id = $1 OR idempotency_key = $2 OR provider_request_key = $3
	`, eventID, idempotencyKey, providerRequestKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ProviderFreezeSyncRecord
	for rows.Next() {
		var rec ProviderFreezeSyncRecord
		var lockedAt *time.Time
		var completedAt *time.Time
		var lastError *string

		if err := rows.Scan(
			&rec.ID, &rec.EventID, &rec.IdempotencyKey, &rec.CardID, &rec.ProviderID, &rec.ProviderCardRef,
			&rec.ProviderRequestKey, &rec.Status, &rec.Attempts, &lastError, &rec.LockedBy, &lockedAt,
			&rec.ClaimToken, &rec.LeaseVersion, &completedAt, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if lastError != nil {
			rec.LastError = *lastError
		}
		rec.LockedAt = lockedAt
		rec.CompletedAt = completedAt
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, pgx.ErrNoRows
	}

	// R21 P0-6: 逐条验证身份一致性，任一冲突行身份不一致都返回 ErrSyncPayloadMismatch
	for i := range records {
		if records[i].EventID != eventID ||
			records[i].IdempotencyKey != idempotencyKey ||
			records[i].CardID != cardID ||
			records[i].ProviderID != providerID ||
			records[i].ProviderCardRef != providerCardRef ||
			records[i].ProviderRequestKey != providerRequestKey {
			return nil, fmt.Errorf("issuing: unique conflict with different identity: %w", ErrSyncPayloadMismatch)
		}
	}

	// R21 P0-6: 优先返回 event_id 匹配的行
	var eventMatch *ProviderFreezeSyncRecord
	for i := range records {
		if records[i].EventID == eventID {
			eventMatch = &records[i]
			break
		}
	}

	if eventMatch != nil {
		return eventMatch, nil
	}

	// 否则返回第一行（idempotency_key 或 provider_request_key 匹配）
	return &records[0], nil
}
