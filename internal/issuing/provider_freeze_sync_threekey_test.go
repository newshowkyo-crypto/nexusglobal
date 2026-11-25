package issuing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// R23 Item 5: 真实三键冲突仓储测试
// 直接构造 dbProviderFreezeSyncRepository，fake Query 返回三条不同记录，
// 分别让 event_id、idempotency_key、provider_request_key 命中不同身份。
// 直接调用 queryExistingByAnyUniqueKey，断言 ErrSyncPayloadMismatch。
// 禁止使用 newMockSyncRepo 完成本项。
// ============================================================================

// multiRowFakeRows 是 pgx.Rows 的 fake 实现，支持返回多条预编程记录。
type multiRowFakeRows struct {
	records [][]any // 每条记录的字段值列表
	idx     int     // 当前读取位置
	closed  bool
}

func (r *multiRowFakeRows) Next() bool {
	if r.closed {
		return false
	}
	r.idx++
	return r.idx <= len(r.records)
}

func (r *multiRowFakeRows) Scan(dest ...any) error {
	if r.idx < 1 || r.idx > len(r.records) {
		return pgx.ErrNoRows
	}
	row := r.records[r.idx-1]
	for i, val := range row {
		if i >= len(dest) {
			break
		}
		switch d := dest[i].(type) {
		case *uuid.UUID:
			if v, ok := val.(uuid.UUID); ok {
				*d = v
			}
		case **uuid.UUID:
			if v, ok := val.(*uuid.UUID); ok {
				*d = v
			} else if val == nil {
				*d = nil
			}
		case *string:
			if v, ok := val.(string); ok {
				*d = v
			}
		case *ProviderFreezeSyncStatus:
			if v, ok := val.(string); ok {
				*d = ProviderFreezeSyncStatus(v)
			}
		case *int:
			if v, ok := val.(int); ok {
				*d = v
			}
		case *int64:
			if v, ok := val.(int64); ok {
				*d = v
			}
		case *time.Time:
			if v, ok := val.(time.Time); ok {
				*d = v
			}
		case **time.Time:
			if v, ok := val.(*time.Time); ok {
				*d = v
			} else if val == nil {
				*d = nil
			}
		case **string:
			if v, ok := val.(*string); ok {
				*d = v
			} else if val == nil {
				*d = nil
			}
		}
	}
	return nil
}

func (r *multiRowFakeRows) Close()                                       { r.closed = true }
func (r *multiRowFakeRows) Err() error                                   { return nil }
func (r *multiRowFakeRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("") }
func (r *multiRowFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *multiRowFakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *multiRowFakeRows) RawValues() [][]byte                          { return nil }
func (r *multiRowFakeRows) Conn() *pgx.Conn                              { return nil }

// threeKeyConflictPool 是 freezeSyncDB 的 fake，Query 返回三条不同身份的记录。
type threeKeyConflictPool struct {
	rows pgx.Rows
}

func (p *threeKeyConflictPool) Begin(ctx context.Context) (pgx.Tx, error) {
	return &fakeFreezeSyncTx{}, nil
}
func (p *threeKeyConflictPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return &fakeFreezeSyncRow{}
}
func (p *threeKeyConflictPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.rows, nil
}
func (p *threeKeyConflictPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

// makeFreezeSyncRecordRow 构造一条 ProviderFreezeSyncRecord 的字段值列表（与 SQL SELECT 顺序一致）
// SELECT id, event_id, idempotency_key, card_id, provider_id, provider_card_ref,
//
//	provider_request_key, status, attempts, last_error, locked_by, locked_at,
//	claim_token, lease_version, completed_at, created_at, updated_at
func makeFreezeSyncRecordRow(
	id uuid.UUID,
	eventID string,
	idempotencyKey string,
	cardID uuid.UUID,
	providerID uuid.UUID,
	providerCardRef string,
	providerRequestKey string,
	status ProviderFreezeSyncStatus,
) []any {
	return []any{
		id,                 // id
		eventID,            // event_id
		idempotencyKey,     // idempotency_key
		cardID,             // card_id
		providerID,         // provider_id
		providerCardRef,    // provider_card_ref
		providerRequestKey, // provider_request_key
		string(status),     // status
		1,                  // attempts
		nil,                // last_error (*string)
		"worker-A",         // locked_by
		nil,                // locked_at (*time.Time)
		uuid.New(),         // claim_token
		int64(1),           // lease_version
		nil,                // completed_at (*time.Time)
		time.Now(),         // created_at
		time.Now(),         // updated_at
	}
}

// TestQueryExistingByAnyUniqueKey_ThreeKeyConflictDifferentIdentity 验证三条唯一键
// 分别命中不同身份的记录时返回 ErrSyncPayloadMismatch。
// 直接构造 dbProviderFreezeSyncRepository，fake Query 返回三条不同记录。
func TestQueryExistingByAnyUniqueKey_ThreeKeyConflictDifferentIdentity(t *testing.T) {
	cardID_A := uuid.New()
	cardID_B := uuid.New()
	cardID_C := uuid.New()
	providerID := uuid.New()

	// 三条记录分别命中 event_id、idempotency_key、provider_request_key
	// 但每条记录的 payload（cardID, providerCardRef）不同
	recA_ID := uuid.New()
	recB_ID := uuid.New()
	recC_ID := uuid.New()

	rows := &multiRowFakeRows{
		records: [][]any{
			// 记录A：event_id="evt-search"，idempotency_key="idem-A"，provider_request_key="prk-A"
			// 命中 event_id 匹配，但 payload 与搜索参数不同
			makeFreezeSyncRecordRow(recA_ID, "evt-search", "idem-A", cardID_A, providerID, "ref-A", "prk-A", FreezeSyncProcessing),
			// 记录B：event_id="evt-B"，idempotency_key="idem-search"，provider_request_key="prk-B"
			// 命中 idempotency_key 匹配，但 payload 不同
			makeFreezeSyncRecordRow(recB_ID, "evt-B", "idem-search", cardID_B, providerID, "ref-B", "prk-B", FreezeSyncProcessing),
			// 记录C：event_id="evt-C"，idempotency_key="idem-C"，provider_request_key="prk-search"
			// 命中 provider_request_key 匹配，但 payload 不同
			makeFreezeSyncRecordRow(recC_ID, "evt-C", "idem-C", cardID_C, providerID, "ref-C", "prk-search", FreezeSyncProcessing),
		},
	}

	pool := &threeKeyConflictPool{rows: rows}
	repo := &dbProviderFreezeSyncRepository{pool: pool}

	// 搜索参数：event_id="evt-search", idempotency_key="idem-search", provider_request_key="prk-search"
	// 但 cardID/providerCardRef 与三条记录都不同 → 应返回 ErrSyncPayloadMismatch
	searchCardID := uuid.New()
	searchProviderCardRef := "ref-search"

	_, err := repo.queryExistingByAnyUniqueKey(
		context.Background(),
		"evt-search",          // eventID
		"idem-search",         // idempotencyKey
		"prk-search",          // providerRequestKey
		searchCardID,          // cardID — 与所有三条记录都不同
		providerID,            // providerID — 相同
		searchProviderCardRef, // providerCardRef — 与所有三条记录都不同
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch,
		"Three records with different identities matching different unique keys must return ErrSyncPayloadMismatch")
}

// TestQueryExistingByAnyUniqueKey_EventIDMatchSameIdentity 验证 event_id 匹配且身份一致时返回该记录。
func TestQueryExistingByAnyUniqueKey_EventIDMatchSameIdentity(t *testing.T) {
	cardID := uuid.New()
	providerID := uuid.New()
	recID := uuid.New()

	rows := &multiRowFakeRows{
		records: [][]any{
			makeFreezeSyncRecordRow(recID, "evt-search", "idem-search", cardID, providerID, "ref-search", "prk-search", FreezeSyncSucceeded),
		},
	}

	pool := &threeKeyConflictPool{rows: rows}
	repo := &dbProviderFreezeSyncRepository{pool: pool}

	rec, err := repo.queryExistingByAnyUniqueKey(
		context.Background(),
		"evt-search",
		"idem-search",
		"prk-search",
		cardID,
		providerID,
		"ref-search",
	)

	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "evt-search", rec.EventID)
	assert.Equal(t, FreezeSyncSucceeded, rec.Status)
}

// TestQueryExistingByAnyUniqueKey_EmptyResult 验证无匹配记录时返回 pgx.ErrNoRows。
func TestQueryExistingByAnyUniqueKey_EmptyResult(t *testing.T) {
	rows := &multiRowFakeRows{
		records: [][]any{}, // 空结果
	}

	pool := &threeKeyConflictPool{rows: rows}
	repo := &dbProviderFreezeSyncRepository{pool: pool}

	_, err := repo.queryExistingByAnyUniqueKey(
		context.Background(),
		"evt-not-found",
		"idem-not-found",
		"prk-not-found",
		uuid.New(),
		uuid.New(),
		"ref-not-found",
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

// TestQueryExistingByAnyUniqueKey_TwoRecordsOneMismatch 验证两条记录中一条身份不一致时返回 ErrSyncPayloadMismatch。
func TestQueryExistingByAnyUniqueKey_TwoRecordsOneMismatch(t *testing.T) {
	cardID_match := uuid.New()
	providerID := uuid.New()
	recID_match := uuid.New()
	recID_mismatch := uuid.New()

	rows := &multiRowFakeRows{
		records: [][]any{
			// 第一条：身份一致
			makeFreezeSyncRecordRow(recID_match, "evt-search", "idem-search", cardID_match, providerID, "ref-search", "prk-search", FreezeSyncSucceeded),
			// 第二条：idempotency_key 匹配但 cardID 不同
			makeFreezeSyncRecordRow(recID_mismatch, "evt-other", "idem-search", uuid.New(), providerID, "ref-other", "prk-other", FreezeSyncProcessing),
		},
	}

	pool := &threeKeyConflictPool{rows: rows}
	repo := &dbProviderFreezeSyncRepository{pool: pool}

	_, err := repo.queryExistingByAnyUniqueKey(
		context.Background(),
		"evt-search",
		"idem-search",
		"prk-search",
		cardID_match,
		providerID,
		"ref-search",
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPayloadMismatch,
		"One mismatched record among multiple must return ErrSyncPayloadMismatch")
}
