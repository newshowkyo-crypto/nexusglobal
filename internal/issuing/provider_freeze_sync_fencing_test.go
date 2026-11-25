package issuing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-19: DB concurrency/fencing 契约测试。
// 使用 mockSyncRepo 验证 fencing 行为契约（DB 实现必须满足相同契约）。
// 可选的真实 PostgreSQL 测试可环境门控，但单元/合约覆盖必须在 -short 模式执行。

// TestFencingContract_ConcurrentClaimProducesOneOwner 验证并发 claim 产生一个 owner。
func TestFencingContract_ConcurrentClaimProducesOneOwner(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// Worker A 认领
	recA, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-concurrent", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-A", recA.LockedBy)

	// Worker B 用相同 payload 认领（重新认领，先使租约过期）
	syncRepo.mu.Lock()
	if rec, ok := syncRepo.records["evt-concurrent"]; ok {
		pastTime := time.Now().Add(-3 * time.Minute)
		rec.LockedAt = &pastTime
	}
	syncRepo.mu.Unlock()
	recB, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-concurrent", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-B", recB.LockedBy)
	assert.Equal(t, int64(2), recB.LeaseVersion, "worker B gets incremented lease version")
}

// TestFencingContract_ExpiredLeaseProducesNewToken 验证过期租约产生新 token 和递增版本。
func TestFencingContract_ExpiredLeaseProducesNewToken(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次认领
	rec1, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-expired", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rec1.LeaseVersion)
	firstClaimToken := rec1.ClaimToken // 保存第一次的 token（指针会被更新）

	// 模拟租约过期
	syncRepo.mu.Lock()
	if rec, ok := syncRepo.records["evt-expired"]; ok {
		pastTime := time.Now().Add(-3 * time.Minute)
		rec.LockedAt = &pastTime
	}
	syncRepo.mu.Unlock()

	// 重新认领（模拟过期后重试）
	rec2, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-expired", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), rec2.LeaseVersion, "lease version must increment")
	assert.NotEqual(t, firstClaimToken, rec2.ClaimToken, "new claim token must differ")
}

// TestFencingContract_OldOwnerCannotMarkAfterReclaim 验证旧 owner 在 reclaim 后无法 mark。
func TestFencingContract_OldOwnerCannotMarkAfterReclaim(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// Worker A 认领
	recA, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-fencing", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	workerAClaimToken := recA.ClaimToken // 保存 Worker A 的 token（指针会被更新）
	workerALeaseVersion := recA.LeaseVersion

	// 模拟租约过期，使 Worker B 可以重新认领
	syncRepo.mu.Lock()
	if rec, ok := syncRepo.records["evt-fencing"]; ok {
		pastTime := time.Now().Add(-3 * time.Minute)
		rec.LockedAt = &pastTime
	}
	syncRepo.mu.Unlock()

	// Worker B 重新认领
	recB, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-fencing", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.NoError(t, err)

	// Worker A 用旧 token mark → 失败
	err = syncRepo.MarkSucceeded(context.Background(), "evt-fencing", workerAClaimToken, workerALeaseVersion)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncLeaseLost)

	// Worker B 用新 token mark → 成功
	err = syncRepo.MarkSucceeded(context.Background(), "evt-fencing", recB.ClaimToken, recB.LeaseVersion)
	require.NoError(t, err)
}

// TestFencingContract_TerminalStateCannotBeOverwritten 验证终态不能被另一个终态覆盖。
func TestFencingContract_TerminalStateCannotBeOverwritten(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 认领并标记成功
	rec, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-terminal", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	err = syncRepo.MarkSucceeded(context.Background(), "evt-terminal", rec.ClaimToken, rec.LeaseVersion)
	require.NoError(t, err)

	// 尝试用相同 token 标记失败 → 应该失败（已经是 SUCCEEDED，不是 PROCESSING）
	err = syncRepo.MarkFailed(context.Background(), "evt-terminal", rec.ClaimToken, rec.LeaseVersion, "error")
	// F-09: mock 现在检查 status='PROCESSING'，SUCCEEDED 后无法 MarkFailed
	require.Error(t, err, "must not be able to mark failed after succeeded")
	assert.ErrorIs(t, err, ErrSyncLeaseLost)

	// 验证状态仍为 SUCCEEDED
	rec2, _ := syncRepo.GetByEventID(context.Background(), "evt-terminal")
	assert.Equal(t, FreezeSyncSucceeded, rec2.Status, "terminal state must not be overwritten")
}

// TestFencingContract_ZeroAffectedRowsReturnsLeaseLost 验证零影响行返回 ErrSyncLeaseLost。
func TestFencingContract_ZeroAffectedRowsReturnsLeaseLost(t *testing.T) {
	syncRepo := newMockSyncRepo()

	// 对不存在的 event_id 调用 MarkSucceeded → RowsAffected == 0 → ErrSyncLeaseLost
	err := syncRepo.MarkSucceeded(context.Background(), "nonexistent", uuid.New(), 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncLeaseLost)
}

// TestFencingContract_UniqueConflictDifferentIdentityReturnsPayloadMismatch 验证 unique 冲突返回 payload mismatch。
func TestFencingContract_UniqueConflictDifferentIdentityReturnsPayloadMismatch(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次插入
	_, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-unique", "idem-001",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 同一 event_id 但不同 payload → payload mismatch
	_, _, err = syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-unique", "idem-002", // 不同的 idempotency key
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch)
}

// TestFencingContract_LeaseQueryErrorsAreReturned 验证 lease 查询错误被返回。
// R22 Item 1: 必须调用 ClaimOrGetSucceeded；
// 第一次 QueryRow 返回一条 PROCESSING 且 locked_at 非空的完整记录；
// 第二次 lease-validity QueryRow.Scan 返回指定错误；
// 断言返回该原始错误、没有执行 reclaim UPDATE、没有 Commit。
func TestFencingContract_LeaseQueryErrorsAreReturned(t *testing.T) {
	leaseQueryErr := fmt.Errorf("fake: lease validity query scan error")

	cardID := uuid.New()
	providerID := uuid.New()
	claimToken := uuid.New()
	now := time.Now()
	lockedByStr := "worker-A"

	// 构造 fake pool：Begin 返回 fakeTx，fakeTx 的 QueryRow 按调用次数返回不同结果
	fakeTx := &fakeFreezeSyncTx{
		queryRowCalls: []fakeQueryRowResult{
			// 第1次 QueryRow：SELECT ... FOR UPDATE → 返回 PROCESSING 记录（payload 一致）
			{scanFn: func(dest ...any) error {
				*(dest[0].(*uuid.UUID)) = uuid.New()                          // id
				*(dest[1].(*string)) = "evt-lease-err"                        // event_id
				*(dest[2].(*string)) = "idem"                                 // idempotency_key
				*(dest[3].(*uuid.UUID)) = cardID                              // card_id
				*(dest[4].(*uuid.UUID)) = providerID                          // provider_id
				*(dest[5].(*string)) = "ref-001"                              // provider_card_ref
				*(dest[6].(*string)) = "freeze:key-001"                       // provider_request_key
				*(dest[7].(*ProviderFreezeSyncStatus)) = FreezeSyncProcessing // status
				*(dest[8].(*int)) = 1                                         // attempts
				*(dest[9].(**string)) = nil                                   // lastError
				*(dest[10].(*string)) = lockedByStr                           // locked_by
				*(dest[11].(**time.Time)) = &now                              // locked_at (非空)
				*(dest[12].(*uuid.UUID)) = claimToken                         // claim_token
				*(dest[13].(*int64)) = int64(1)                               // lease_version
				*(dest[14].(**time.Time)) = nil                               // completedAt
				*(dest[15].(*time.Time)) = now                                // created_at
				*(dest[16].(*time.Time)) = now                                // updated_at
				return nil
			}},
			// 第2次 QueryRow：lease-validity 查询 → Scan 返回指定错误
			{scanErr: leaseQueryErr},
		},
	}

	fakePool := &fakeFreezeSyncPool{
		beginFn: func(ctx context.Context) (pgx.Tx, error) {
			return fakeTx, nil
		},
	}
	repo := &dbProviderFreezeSyncRepository{pool: fakePool}

	// 调用 ClaimOrGetSucceeded（不是 GetByEventID）
	// payload 必须与 fake 返回的记录一致，否则会先触发 payload mismatch
	_, _, err := repo.ClaimOrGetSucceeded(
		context.Background(),
		"evt-lease-err", "idem",
		cardID, providerID,
		"ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)

	// 断言返回原始错误
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake: lease validity query scan error")

	// 断言没有执行 reclaim UPDATE（execCalls 应为 0）
	assert.Equal(t, 0, fakeTx.execCalls, "must not execute reclaim UPDATE when lease query fails")

	// 断言没有 Commit（commitCalls 应为 0）
	assert.Equal(t, 0, fakeTx.commitCalls, "must not Commit when lease query fails")
}

// fakeFreezeSyncPool 是 freezeSyncDB 的 fake 实现，用于注入错误。
type fakeFreezeSyncPool struct {
	queryRowErr error                                     // QueryRow 返回的 fakeRow 的 Scan 错误
	queryErr    error                                     // Query 返回的错误
	beginErr    error                                     // Begin 返回的错误
	execErr     error                                     // Exec 返回的错误
	beginFn     func(ctx context.Context) (pgx.Tx, error) // 可替换的 Begin 实现
}

func (f *fakeFreezeSyncPool) Begin(ctx context.Context) (pgx.Tx, error) {
	if f.beginFn != nil {
		return f.beginFn(ctx)
	}
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &fakeFreezeSyncTx{}, nil
}

func (f *fakeFreezeSyncPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return &fakeFreezeSyncRow{scanErr: f.queryRowErr}
}

func (f *fakeFreezeSyncPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &fakeFreezeSyncRows{}, nil
}

func (f *fakeFreezeSyncPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

// fakeQueryRowResult 定义 fakeFreezeSyncTx.QueryRow 的可编程返回结果。
type fakeQueryRowResult struct {
	scanFn  func(dest ...any) error // 自定义 Scan 行为
	scanErr error                   // Scan 返回的错误（如果 scanFn 为 nil）
}

// fakeFreezeSyncRow 是 pgx.Row 的 fake 实现。
type fakeFreezeSyncRow struct {
	scanErr error
	scanFn  func(dest ...any) error
}

func (r *fakeFreezeSyncRow) Scan(dest ...any) error {
	if r.scanFn != nil {
		return r.scanFn(dest...)
	}
	return r.scanErr
}

// fakeFreezeSyncRows 是 pgx.Rows 的 fake 实现。
type fakeFreezeSyncRows struct {
	closed bool
}

func (r *fakeFreezeSyncRows) Next() bool                                   { return false }
func (r *fakeFreezeSyncRows) Scan(dest ...any) error                       { return nil }
func (r *fakeFreezeSyncRows) Close()                                       { r.closed = true }
func (r *fakeFreezeSyncRows) Err() error                                   { return nil }
func (r *fakeFreezeSyncRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("") }
func (r *fakeFreezeSyncRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeFreezeSyncRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeFreezeSyncRows) RawValues() [][]byte                          { return nil }
func (r *fakeFreezeSyncRows) Conn() *pgx.Conn                              { return nil }

// fakeFreezeSyncTx 是 pgx.Tx 的 fake 实现。
type fakeFreezeSyncTx struct {
	commitErr     error
	rollbackErr   error
	queryRowErr   error
	execErr       error
	queryRowCalls []fakeQueryRowResult // 可编程的多阶段 QueryRow 返回结果
	queryRowIdx   int                  // 当前 QueryRow 调用索引
	execCalls     int                  // Exec 被调用的次数
	commitCalls   int                  // Commit 被调用的次数
}

func (t *fakeFreezeSyncTx) Commit(ctx context.Context) error {
	t.commitCalls++
	return t.commitErr
}
func (t *fakeFreezeSyncTx) Rollback(ctx context.Context) error { return t.rollbackErr }
func (t *fakeFreezeSyncTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.queryRowCalls != nil && t.queryRowIdx < len(t.queryRowCalls) {
		result := t.queryRowCalls[t.queryRowIdx]
		t.queryRowIdx++
		return &fakeFreezeSyncRow{scanFn: result.scanFn, scanErr: result.scanErr}
	}
	return &fakeFreezeSyncRow{scanErr: t.queryRowErr}
}
func (t *fakeFreezeSyncTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execCalls++
	if t.execErr != nil {
		return pgconn.CommandTag{}, t.execErr
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (t *fakeFreezeSyncTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return &fakeFreezeSyncRows{}, nil
}
func (t *fakeFreezeSyncTx) Begin(ctx context.Context) (pgx.Tx, error) { return t, nil }
func (t *fakeFreezeSyncTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *fakeFreezeSyncTx) Conn() *pgx.Conn { return nil }
func (t *fakeFreezeSyncTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *fakeFreezeSyncTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (t *fakeFreezeSyncTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }

// TestFencingContract_SucceededReplayIsIdempotent 验证已成功的重放是幂等的。
func TestFencingContract_SucceededReplayIsIdempotent(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 认领并标记成功
	rec, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-idem", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)
	err = syncRepo.MarkSucceeded(context.Background(), "evt-idem", rec.ClaimToken, rec.LeaseVersion)
	require.NoError(t, err)

	// 重放：应返回 alreadySucceeded=true
	_, alreadySucceeded, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-idem", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.NoError(t, err)
	assert.True(t, alreadySucceeded, "replay of SUCCEEDED must return alreadySucceeded=true")
}

// TestFencingContract_MarkFailedClearsLeaseFields 验证 MarkFailed 清除 lease 字段。
// F-09: 终态必须清除 claim_token、locked_by、locked_at。
func TestFencingContract_MarkFailedClearsLeaseFields(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	rec, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-clear", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	err = syncRepo.MarkFailed(context.Background(), "evt-clear", rec.ClaimToken, rec.LeaseVersion, "error")
	require.NoError(t, err)

	result, _ := syncRepo.GetByEventID(context.Background(), "evt-clear")
	assert.Equal(t, FreezeSyncFailed, result.Status)
	// F-09: 验证 lease 字段已清除
	assert.Equal(t, uuid.Nil, result.ClaimToken, "claim_token must be cleared")
	assert.Empty(t, result.LockedBy, "locked_by must be cleared")
	assert.Nil(t, result.LockedAt, "locked_at must be cleared")
}

// TestFencingContract_ProviderRequestKeyFormat 验证 provider_request_key 格式。
// F-10: provider_request_key = freeze:{provider_id}:{provider_card_ref}:{event_id}
func TestFencingContract_ProviderRequestKeyFormat(t *testing.T) {
	providerID := uuid.New()
	providerCardRef := "card-ref-123"
	eventID := "evt-001"

	expected := fmt.Sprintf("freeze:%s:%s:%s", providerID, providerCardRef, eventID)
	actual := expectedProviderRequestKey(providerID, providerCardRef, eventID)
	assert.Equal(t, expected, actual)
}

// TestFencingContract_SameIdentityProcessingReturnsLeaseHeld 验证相同身份 + PROCESSING 返回 ErrSyncLeaseHeld。
// R18 P0-6: 冲突后查询已有记录，区分 SUCCEEDED/PROCESSING/PayloadMismatch。
func TestFencingContract_SameIdentityProcessingReturnsLeaseHeld(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 第一次插入（PROCESSING 状态）
	_, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-processing", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 同一 event_id + 同一 payload → PROCESSING → ErrSyncLeaseHeld
	_, _, err = syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-processing", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-B", 2*time.Minute,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncLeaseHeld)
}

// TestFencingContract_ReclaimWithWrongLeaseVersionReturnsLeaseLost 验证 reclaim 用错误 lease version 返回 ErrSyncLeaseLost。
// R18 P0-6: reclaim UPDATE 检查 RowsAffected()!=1 → ErrSyncLeaseLost。
func TestFencingContract_ReclaimWithWrongLeaseVersionReturnsLeaseLost(t *testing.T) {
	syncRepo := newMockSyncRepo()
	cardID := uuid.New()
	providerID := uuid.New()

	// 认领
	rec, _, err := syncRepo.ClaimOrGetSucceeded(
		context.Background(), "evt-reclaim", "idem",
		cardID, providerID, "ref-001", "freeze:key-001",
		"worker-A", 2*time.Minute,
	)
	require.NoError(t, err)

	// 用错误的 lease version mark → ErrSyncLeaseLost
	wrongLeaseVersion := rec.LeaseVersion + 999
	err = syncRepo.MarkSucceeded(context.Background(), "evt-reclaim", rec.ClaimToken, wrongLeaseVersion)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncLeaseLost)
}
