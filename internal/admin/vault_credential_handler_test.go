package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/platform/crypto"
	"github.com/nexusglobal/mamr/internal/platform/httpx"
)

// ============================================================================
// R20 P1-3: Vault 写入 API 不回显秘密测试
// ============================================================================

// TestVaultCredentialWriteResponse_NoSecretFields 验证 VaultCredentialWriteResponse
// 结构体不包含任何秘密字段（plaintext、ciphertext、nonce、aad 等）。
func TestVaultCredentialWriteResponse_NoSecretFields(t *testing.T) {
	resp := VaultCredentialWriteResponse{
		VaultKey:       "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		RecordID:       "660e8400-e29b-41d4-a716-446655440001",
		KeyVersion:     1,
		CredentialType: "API_KEY",
		Status:         "ACTIVE",
		CreatedAt:      "2026-09-09T12:00:00Z",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	jsonStr := string(data)

	// 响应中不得包含任何秘密字段
	prohibitedFields := []string{
		"plaintext",
		"ciphertext",
		"nonce",
		"aad",
		"aad_context",
		"secret",
		"api_key_value",
		"credential_material",
		"raw_key",
		"encrypted",
	}

	for _, field := range prohibitedFields {
		if strings.Contains(strings.ToLower(jsonStr), strings.ToLower(field)) {
			t.Errorf("VaultCredentialWriteResponse JSON contains prohibited secret field: %s (in: %s)", field, jsonStr)
		}
	}

	// 响应必须包含 vault:// 引用
	if !strings.Contains(jsonStr, "vault://") {
		t.Error("VaultCredentialWriteResponse must contain vault:// reference")
	}

	// 响应必须包含 record_id
	if !strings.Contains(jsonStr, "record_id") {
		t.Error("VaultCredentialWriteResponse must contain record_id")
	}
}

// TestVaultCredentialWriteRequest_OnlySafeFields 验证请求体只接受安全字段
// （issuing_provider_id、credential_type、credential_material），
// 不接受 tenant_id、adapter_provider_id 等 ownership 字段（由 DB 推导）。
func TestVaultCredentialWriteRequest_OnlySafeFields(t *testing.T) {
	jsonStr := `{
		"issuing_provider_id": "550e8400-e29b-41d4-a716-446655440000",
		"credential_type": "API_KEY",
		"credential_material": {"api_key": "sk_test_123"}
	}`

	var req VaultCredentialWriteRequest
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	if req.IssuingProviderID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("IssuingProviderID: got %q", req.IssuingProviderID)
	}
	if req.CredentialType != "API_KEY" {
		t.Errorf("CredentialType: got %q", req.CredentialType)
	}
	if len(req.CredentialMaterial) == 0 {
		t.Error("CredentialMaterial should not be empty")
	}

	// 验证结构体没有 tenant_id、adapter_provider_id 字段
	// （这些必须由 DB 推导，客户端不能提交）
	reqJSON, _ := json.Marshal(req)
	if strings.Contains(string(reqJSON), "tenant_id") {
		t.Error("VaultCredentialWriteRequest must not contain tenant_id (DB-derived only)")
	}
	if strings.Contains(string(reqJSON), "adapter_provider_id") {
		t.Error("VaultCredentialWriteRequest must not contain adapter_provider_id (DB-derived only)")
	}
}

// ============================================================================
// R22 Item 6: Vault Handler 行为测试 — 使用 fake vaultDBConn/vaultEncryptor
// ============================================================================

// --- Fake 实现 ---

// fakeVaultRow 是 pgx.Row 的 fake 实现，支持可编程 Scan 行为。
type fakeVaultRow struct {
	scanFn  func(dest ...any) error
	scanErr error
}

func (r *fakeVaultRow) Scan(dest ...any) error {
	if r.scanFn != nil {
		return r.scanFn(dest...)
	}
	return r.scanErr
}

// fakeVaultRows 是 pgx.Rows 的 fake 实现。
type fakeVaultRows struct{}

func (r *fakeVaultRows) Next() bool                                   { return false }
func (r *fakeVaultRows) Scan(dest ...any) error                       { return nil }
func (r *fakeVaultRows) Close()                                       {}
func (r *fakeVaultRows) Err() error                                   { return nil }
func (r *fakeVaultRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("") }
func (r *fakeVaultRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeVaultRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeVaultRows) RawValues() [][]byte                          { return nil }
func (r *fakeVaultRows) Conn() *pgx.Conn                              { return nil }

// fakeVaultTx 是 pgx.Tx 的 fake 实现，支持可编程多阶段 QueryRow。
type fakeVaultTx struct {
	// 可编程的 QueryRow 返回序列（按调用顺序消费）
	queryRowResults []fakeVaultTxRowResult
	queryRowIdx     int

	// 计数器
	execCalls     int
	commitCalls   int
	rollbackCalls int

	// 错误注入
	execErr     error
	commitErr   error
	rollbackErr error
}

type fakeVaultTxRowResult struct {
	scanFn  func(dest ...any) error
	scanErr error
}

func (t *fakeVaultTx) Commit(ctx context.Context) error {
	t.commitCalls++
	return t.commitErr
}

func (t *fakeVaultTx) Rollback(ctx context.Context) error {
	t.rollbackCalls++
	return t.rollbackErr
}

func (t *fakeVaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.queryRowIdx < len(t.queryRowResults) {
		result := t.queryRowResults[t.queryRowIdx]
		t.queryRowIdx++
		return &fakeVaultRow{scanFn: result.scanFn, scanErr: result.scanErr}
	}
	return &fakeVaultRow{scanErr: pgx.ErrNoRows}
}

func (t *fakeVaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execCalls++
	if t.execErr != nil {
		return pgconn.CommandTag{}, t.execErr
	}
	return pgconn.NewCommandTag("INSERT 1"), nil
}

func (t *fakeVaultTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return &fakeVaultRows{}, nil
}

func (t *fakeVaultTx) Begin(ctx context.Context) (pgx.Tx, error) { return t, nil }

func (t *fakeVaultTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (t *fakeVaultTx) Conn() *pgx.Conn { return nil }

func (t *fakeVaultTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}

func (t *fakeVaultTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }

func (t *fakeVaultTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }

// fakeVaultDBConn 是 vaultDBConn 的 fake 实现。
type fakeVaultDBConn struct {
	// 可编程的 QueryRow 返回序列（按调用顺序消费）
	queryRowResults []fakeVaultDBRowResult
	queryRowIdx     int

	// Begin 返回的 fake tx
	beginTx *fakeVaultTx
	beginFn func(ctx context.Context) (pgx.Tx, error)

	// 计数器
	beginCalls int

	// 错误注入
	beginErr error
}

type fakeVaultDBRowResult struct {
	scanFn  func(dest ...any) error
	scanErr error
}

func (d *fakeVaultDBConn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if d.queryRowIdx < len(d.queryRowResults) {
		result := d.queryRowResults[d.queryRowIdx]
		d.queryRowIdx++
		return &fakeVaultRow{scanFn: result.scanFn, scanErr: result.scanErr}
	}
	return &fakeVaultRow{scanErr: pgx.ErrNoRows}
}

func (d *fakeVaultDBConn) Begin(ctx context.Context) (pgx.Tx, error) {
	d.beginCalls++
	if d.beginFn != nil {
		return d.beginFn(ctx)
	}
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	return d.beginTx, nil
}

// fakeVaultEncryptor 是 vaultEncryptor 的 fake 实现。
type fakeVaultEncryptor struct {
	encryptFn  func(ctx context.Context, plaintext []byte, meta crypto.Meta) ([]byte, error)
	encryptErr error
	// 返回的加密数据（nonce(12) + ciphertext）
	encryptResult []byte
	encryptCalls  int
}

func (e *fakeVaultEncryptor) Encrypt(ctx context.Context, plaintext []byte, meta crypto.Meta) ([]byte, error) {
	e.encryptCalls++
	if e.encryptFn != nil {
		return e.encryptFn(ctx, plaintext, meta)
	}
	if e.encryptErr != nil {
		return nil, e.encryptErr
	}
	return e.encryptResult, nil
}

// --- 测试辅助 ---

// vaultTestFixtures 包含测试中常用的 UUID 和固定值。
type vaultTestFixtures struct {
	issuingProviderID uuid.UUID
	entityID          uuid.UUID
	adapterProviderID uuid.UUID
	tenantID          uuid.UUID
}

func newVaultTestFixtures() vaultTestFixtures {
	return vaultTestFixtures{
		issuingProviderID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		entityID:          uuid.MustParse("660e8400-e29b-41d4-a716-446655440001"),
		adapterProviderID: uuid.MustParse("770e8400-e29b-41d4-a716-446655440002"),
		tenantID:          uuid.MustParse("880e8400-e29b-41d4-a716-446655440003"),
	}
}

// makeVaultRequest 构造一个合法的 Vault 凭证写入 HTTP 请求，带 admin identity context。
func makeVaultRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/v3/issuing/vault-credentials", strings.NewReader(body))
	identity := &httpx.AdminIdentity{
		Subject:     "admin-test",
		Username:    "testadmin",
		Role:        "ADMIN",
		TenantID:    "test-tenant",
		Permissions: []string{httpx.PermAdminCredentialWrite},
	}
	ctx := context.WithValue(req.Context(), httpx.AdminContextKey{}, identity)
	return req.WithContext(ctx)
}

// makeValidVaultRequestBody 构造合法请求体 JSON。
func makeValidVaultRequestBody(fix vaultTestFixtures) string {
	return `{"issuing_provider_id":"` + fix.issuingProviderID.String() + `","credential_type":"API_KEY","credential_material":{"api_key":"sk_test_123"}}`
}

// makeInitialProviderScanFn 返回初始 provider 查询的 Scan 函数（返回 ACTIVE + 有效 binding）。
// handler 中声明 var entityID *uuid.UUID 然后 Scan(&entityID, ...)，
// 所以 dest 收到的是 **uuid.UUID。
func makeInitialProviderScanFn(fix vaultTestFixtures) func(dest ...any) error {
	return func(dest ...any) error {
		// dest[0] = **uuid.UUID (entity_id)
		// dest[1] = **uuid.UUID (adapter_provider_id)
		// dest[2] = **uuid.UUID (tenant_id)
		// dest[3] = *string (status)
		*(dest[0].(**uuid.UUID)) = &fix.entityID
		*(dest[1].(**uuid.UUID)) = &fix.adapterProviderID
		*(dest[2].(**uuid.UUID)) = &fix.tenantID
		*(dest[3].(*string)) = "ACTIVE"
		return nil
	}
}

// makeInitialAdapterScanFn 返回初始 adapter_provider 查询的 Scan 函数（返回 ACTIVE CARD_ISSUER）。
func makeInitialAdapterScanFn() func(dest ...any) error {
	return func(dest ...any) error {
		// dest[0] = *string (status)
		// dest[1] = *string (adapter_type)
		*(dest[0].(*string)) = "ACTIVE"
		*(dest[1].(*string)) = "CARD_ISSUER"
		return nil
	}
}

// makeRecheckProviderScanFn 返回事务内 recheck provider 的 Scan 函数。
// handler 中声明 var recheckEntityID *uuid.UUID 然后 Scan(&recheckEntityID, ...)，
// 所以 dest 收到的是 **uuid.UUID。
func makeRecheckProviderScanFn(fix vaultTestFixtures, status string) func(dest ...any) error {
	return func(dest ...any) error {
		// dest[0] = **uuid.UUID (entity_id)
		// dest[1] = **uuid.UUID (adapter_provider_id)
		// dest[2] = *string (status)
		*(dest[0].(**uuid.UUID)) = &fix.entityID
		*(dest[1].(**uuid.UUID)) = &fix.adapterProviderID
		*(dest[2].(*string)) = status
		return nil
	}
}

// makeRecheckTenantScanFn 返回事务内 recheck tenant 的 Scan 函数。
// handler 中声明 var recheckTenantID *uuid.UUID 然后 Scan(&recheckTenantID, ...)，
// 所以 dest 收到的是 **uuid.UUID。
func makeRecheckTenantScanFn(tenantID uuid.UUID) func(dest ...any) error {
	return func(dest ...any) error {
		// dest[0] = **uuid.UUID (tenant_id)
		*(dest[0].(**uuid.UUID)) = &tenantID
		return nil
	}
}

// makeRecheckAdapterScanFn 返回事务内 recheck adapter_provider 的 Scan 函数。
func makeRecheckAdapterScanFn(status, adapterType string) func(dest ...any) error {
	return func(dest ...any) error {
		// dest[0] = *string (status)
		// dest[1] = *string (adapter_type)
		*(dest[0].(*string)) = status
		*(dest[1].(*string)) = adapterType
		return nil
	}
}

// makeMaxVersionScanFn 返回 MAX(key_version) 查询的 Scan 函数
// （8b 事务外预查与 10e 事务内重验共用，dest[0] = *int）。
func makeMaxVersionScanFn(maxVersion int) func(dest ...any) error {
	return func(dest ...any) error {
		*(dest[0].(*int)) = maxVersion
		return nil
	}
}

// makeValidEncryptResult 构造合法的加密输出（12 bytes nonce + 16+ bytes ciphertext）。
func makeValidEncryptResult() []byte {
	result := make([]byte, 28) // 12 (nonce) + 16 (ciphertext)
	for i := range result {
		result[i] = byte(i + 1)
	}
	return result
}

// --- 测试场景 ---

// TestVaultCredentialHandler_WriteCredential_Success 验证成功写入且不回显秘密。
// 场景1: fake DB 返回有效数据，fake encryptor 返回有效加密结果，
// fake tx 的 recheck 一致，Exec 成功，Commit 成功。
// 验证响应 201，响应体不含 api_key/ciphertext/nonce/aad。
func TestVaultCredentialHandler_WriteCredential_Success(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			// 10a. recheck issuing_providers FOR UPDATE → 一致
			{scanFn: makeRecheckProviderScanFn(fix, "ACTIVE")},
			// 10b. recheck corporate_entity FOR SHARE → tenant 一致
			{scanFn: makeRecheckTenantScanFn(fix.tenantID)},
			// 10c. recheck adapter_provider FOR SHARE → ACTIVE CARD_ISSUER
			{scanFn: makeRecheckAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 10e. 事务内重验 MAX(key_version) → 未变化（< keyVersion）
			{scanFn: makeMaxVersionScanFn(0)},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			// 6. 初始 provider 查询 → 有效
			{scanFn: makeInitialProviderScanFn(fix)},
			// 8. 初始 adapter_provider 查询 → ACTIVE CARD_ISSUER
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version) → 0
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "应返回 201")

	// 验证响应体不含秘密字段（注意：vault_key 中合法包含 "api_key" 作为路径段，
	// 所以检查 JSON 字段名模式而非简单子串）
	body := rec.Body.String()
	prohibitedFieldPatterns := []string{
		`"ciphertext"`,
		`"nonce"`,
		`"aad"`,
		`"aad_context"`,
		`"plaintext"`,
		`"credential_material"`,
		`"api_key":"`, // api_key 作为 JSON 字段名+值（如 "api_key":"sk_..."）
	}
	for _, pattern := range prohibitedFieldPatterns {
		assert.NotContains(t, body, pattern,
			"响应体不得包含秘密字段模式 %s", pattern)
	}

	// 验证响应包含 vault:// 引用
	assert.Contains(t, body, "vault://")
	assert.Contains(t, body, "record_id")

	// 验证事务行为
	// Exec 共 4 次：10d advisory lock、11 INSERT、11b 绑定 UPDATE、11c 旧版本置 REVOKED
	assert.Equal(t, 4, fakeTx.execCalls, "Exec 应被调用 4 次（锁/插入/绑定/轮换）")
	assert.Equal(t, 1, fakeTx.commitCalls, "Commit 应被调用一次")
	// defer tx.Rollback 在 Commit 成功后仍会被调用（pgx 中为 no-op），
	// 但关键是 Commit 被调用了且 Exec 被调用了
}

// TestVaultCredentialHandler_WriteCredential_EncryptFailureNoBegin 验证加密失败时 Begin/INSERT 均未调用。
// 场景2: fake encryptor 返回错误。验证 500 响应，fake DB 的 Begin 未被调用。
func TestVaultCredentialHandler_WriteCredential_EncryptFailureNoBegin(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version)
			{scanFn: makeMaxVersionScanFn(0)},
		},
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptErr: assertError("fake: encryption failed"),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, "加密失败应返回 500")
	assert.Equal(t, 0, fakeDB.beginCalls, "加密失败时 Begin 不应被调用")
	assert.Equal(t, 1, fakeEnc.encryptCalls, "Encrypt 应被调用一次")
}

// TestVaultCredentialHandler_WriteCredential_TenantChangedRollback 验证事务内 tenant 改变时 Rollback。
// 场景3: fake tx 的 recheck QueryRow 返回与初始不同的 tenantID。
// 验证 CONFIG_INCONSISTENT 错误，tx.Rollback 被调用，tx.Commit 未被调用。
func TestVaultCredentialHandler_WriteCredential_TenantChangedRollback(t *testing.T) {
	fix := newVaultTestFixtures()
	differentTenantID := uuid.MustParse("990e8400-e29b-41d4-a716-446655440099")

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			// 10a. recheck provider → 一致
			{scanFn: makeRecheckProviderScanFn(fix, "ACTIVE")},
			// 10b. recheck tenant → 返回不同的 tenantID
			{scanFn: makeRecheckTenantScanFn(differentTenantID)},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version)
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "tenant 变化应返回 CONFIG_INCONSISTENT (422)")
	body := rec.Body.String()
	assert.Contains(t, body, "CONFIG_INCONSISTENT")
	assert.Contains(t, body, "tenant changed")

	// 验证事务行为
	assert.Equal(t, 0, fakeTx.commitCalls, "tenant 变化时 Commit 不应被调用")
	assert.Equal(t, 0, fakeTx.execCalls, "tenant 变化时 Exec 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_ProviderNotActiveRollback 验证 provider 变为非 ACTIVE 时 Rollback。
// 场景4: fake tx 的 recheck QueryRow 返回 status != "ACTIVE"。
// 验证 CONFIG_INCONSISTENT 错误，Rollback 被调用。
func TestVaultCredentialHandler_WriteCredential_ProviderNotActiveRollback(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			// 10a. recheck provider → status 变为 SUSPENDED
			{scanFn: makeRecheckProviderScanFn(fix, "SUSPENDED")},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version)
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "provider 非 ACTIVE 应返回 CONFIG_INCONSISTENT (422)")
	body := rec.Body.String()
	assert.Contains(t, body, "CONFIG_INCONSISTENT")
	assert.Contains(t, body, "status changed")

	assert.Equal(t, 0, fakeTx.commitCalls, "provider 非 ACTIVE 时 Commit 不应被调用")
	assert.Equal(t, 0, fakeTx.execCalls, "provider 非 ACTIVE 时 Exec 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_AdapterNotActiveRollback 验证 adapter 变为非 ACTIVE 时 Rollback。
// 场景5a: fake tx 的 recheck adapter QueryRow 返回非 ACTIVE。
func TestVaultCredentialHandler_WriteCredential_AdapterNotActiveRollback(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			// 10a. recheck provider → 一致
			{scanFn: makeRecheckProviderScanFn(fix, "ACTIVE")},
			// 10b. recheck tenant → 一致
			{scanFn: makeRecheckTenantScanFn(fix.tenantID)},
			// 10c. recheck adapter → 非 ACTIVE
			{scanFn: makeRecheckAdapterScanFn("SUSPENDED", "CARD_ISSUER")},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version)
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "adapter 非 ACTIVE 应返回 CONFIG_INCONSISTENT (422)")
	body := rec.Body.String()
	assert.Contains(t, body, "CONFIG_INCONSISTENT")
	assert.Contains(t, body, "adapter_provider status changed")

	assert.Equal(t, 0, fakeTx.commitCalls, "adapter 非 ACTIVE 时 Commit 不应被调用")
	assert.Equal(t, 0, fakeTx.execCalls, "adapter 非 ACTIVE 时 Exec 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_AdapterNotCardIssuerRollback 验证 adapter 变为非 CARD_ISSUER 时 Rollback。
// 场景5b: fake tx 的 recheck adapter QueryRow 返回 ACTIVE 但 adapter_type != CARD_ISSUER。
func TestVaultCredentialHandler_WriteCredential_AdapterNotCardIssuerRollback(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			{scanFn: makeRecheckProviderScanFn(fix, "ACTIVE")},
			{scanFn: makeRecheckTenantScanFn(fix.tenantID)},
			// 10c. recheck adapter → ACTIVE 但非 CARD_ISSUER
			{scanFn: makeRecheckAdapterScanFn("ACTIVE", "PAYMENT_PROCESSOR")},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version)
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "adapter 非 CARD_ISSUER 应返回 CONFIG_INCONSISTENT (422)")
	body := rec.Body.String()
	assert.Contains(t, body, "CONFIG_INCONSISTENT")
	assert.Contains(t, body, "adapter_type changed")

	assert.Equal(t, 0, fakeTx.commitCalls, "adapter 非 CARD_ISSUER 时 Commit 不应被调用")
	assert.Equal(t, 0, fakeTx.execCalls, "adapter 非 CARD_ISSUER 时 Exec 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_VersionConflict 验证事务内重验发现并发已写入
// 更高 key_version 时返回 409 IDEMPOTENCY_CONFLICT（写入即轮换的并发兜底）。
// 场景: 8b 预查 MAX=0，事务内 10e 重验 MAX=5（≥ keyVersion=1）。
// 验证 409 响应，Commit 未被调用；advisory lock（10d）在 10e 之前已 Exec 一次。
func TestVaultCredentialHandler_WriteCredential_VersionConflict(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeTx := &fakeVaultTx{
		queryRowResults: []fakeVaultTxRowResult{
			// 10a. recheck provider → 一致
			{scanFn: makeRecheckProviderScanFn(fix, "ACTIVE")},
			// 10b. recheck tenant → 一致
			{scanFn: makeRecheckTenantScanFn(fix.tenantID)},
			// 10c. recheck adapter → ACTIVE CARD_ISSUER
			{scanFn: makeRecheckAdapterScanFn("ACTIVE", "CARD_ISSUER")},
			// 10e. 重验 MAX → 并发已写入版本 5（≥ keyVersion=1）
			{scanFn: makeMaxVersionScanFn(5)},
		},
	}

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
			// 8b. 事务外预查 MAX(key_version) → 0
			{scanFn: makeMaxVersionScanFn(0)},
		},
		beginTx: fakeTx,
	}

	fakeEnc := &fakeVaultEncryptor{
		encryptResult: makeValidEncryptResult(),
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code, "版本冲突应返回 409")
	body := rec.Body.String()
	assert.Contains(t, body, "IDEMPOTENCY_CONFLICT")
	assert.Contains(t, body, "version conflict")

	assert.Equal(t, 0, fakeTx.commitCalls, "版本冲突时 Commit 不应被调用")
	assert.Equal(t, 1, fakeTx.execCalls, "仅 advisory lock Exec 应已发生（10d 在 10e 之前）")
}

// TestVaultCredentialHandler_WriteCredential_UnknownFieldReturns400 验证未知 credential 字段返回 400 且零 DB 写入。
// 场景6: 请求体包含 "extra_field": "value"，由于 DisallowUnknownFields，JSON 解码失败。
// 验证 400 响应，fake DB 的 QueryRow 和 Begin 均未被调用。
func TestVaultCredentialHandler_WriteCredential_UnknownFieldReturns400(t *testing.T) {
	fix := newVaultTestFixtures()

	// 请求体包含未知字段 extra_field
	body := `{"issuing_provider_id":"` + fix.issuingProviderID.String() + `","credential_type":"API_KEY","credential_material":{"api_key":"sk_test_123"},"extra_field":"value"}`

	fakeDB := &fakeVaultDBConn{}
	fakeEnc := &fakeVaultEncryptor{}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, body)
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "未知字段应返回 400")

	// 验证零 DB 写入
	assert.Equal(t, 0, fakeDB.queryRowIdx, "未知字段时 DB QueryRow 不应被调用")
	assert.Equal(t, 0, fakeDB.beginCalls, "未知字段时 DB Begin 不应被调用")
	assert.Equal(t, 0, fakeEnc.encryptCalls, "未知字段时 Encrypt 不应被调用")
}

// --- 额外边界场景 ---

// TestVaultCredentialHandler_WriteCredential_NoAdminIdentity 验证缺少 admin identity 时返回 403。
func TestVaultCredentialHandler_WriteCredential_NoAdminIdentity(t *testing.T) {
	fakeDB := &fakeVaultDBConn{}
	fakeEnc := &fakeVaultEncryptor{}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := httptest.NewRequest(http.MethodPost, "/admin/api/v3/issuing/vault-credentials",
		strings.NewReader(makeValidVaultRequestBody(newVaultTestFixtures())))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, "缺少 admin identity 应返回 403")
}

// TestVaultCredentialHandler_WriteCredential_NilEncryptorFailClosed 验证 Encryptor 缺失时 fail-closed。
func TestVaultCredentialHandler_WriteCredential_NilEncryptorFailClosed(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			{scanFn: makeInitialAdapterScanFn()},
		},
	}

	handler := &VaultCredentialHandler{db: fakeDB, enc: nil}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, "Encryptor 缺失应返回 500 (fail-closed)")
	body := rec.Body.String()
	assert.Contains(t, body, "fail-closed")
	assert.Equal(t, 0, fakeDB.beginCalls, "Encryptor 缺失时 Begin 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_InvalidUUID 验证无效 UUID 返回 400。
func TestVaultCredentialHandler_WriteCredential_InvalidUUID(t *testing.T) {
	fakeDB := &fakeVaultDBConn{}
	fakeEnc := &fakeVaultEncryptor{}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	body := `{"issuing_provider_id":"not-a-uuid","credential_type":"API_KEY","credential_material":{"api_key":"sk_test_123"}}`
	req := makeVaultRequest(t, body)
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "无效 UUID 应返回 400")
	assert.Equal(t, 0, fakeDB.queryRowIdx, "无效 UUID 时 DB QueryRow 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_ProviderNotFound 验证 provider 不存在时返回 404。
func TestVaultCredentialHandler_WriteCredential_ProviderNotFound(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanErr: pgx.ErrNoRows},
		},
	}

	fakeEnc := &fakeVaultEncryptor{}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "provider 不存在应返回 404")
	assert.Equal(t, 0, fakeDB.beginCalls, "provider 不存在时 Begin 不应被调用")
}

// TestVaultCredentialHandler_WriteCredential_AdapterNotActiveInitial 验证初始 adapter 非 ACTIVE 时返回 CONFIG_INCONSISTENT。
func TestVaultCredentialHandler_WriteCredential_AdapterNotActiveInitial(t *testing.T) {
	fix := newVaultTestFixtures()

	fakeDB := &fakeVaultDBConn{
		queryRowResults: []fakeVaultDBRowResult{
			{scanFn: makeInitialProviderScanFn(fix)},
			// 初始 adapter 查询 → 非 ACTIVE
			{scanFn: makeRecheckAdapterScanFn("SUSPENDED", "CARD_ISSUER")},
		},
	}

	fakeEnc := &fakeVaultEncryptor{}

	handler := &VaultCredentialHandler{db: fakeDB, enc: fakeEnc}
	req := makeVaultRequest(t, makeValidVaultRequestBody(fix))
	rec := httptest.NewRecorder()

	handler.WriteCredential(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "初始 adapter 非 ACTIVE 应返回 CONFIG_INCONSISTENT (422)")
	assert.Equal(t, 0, fakeDB.beginCalls, "初始 adapter 非 ACTIVE 时 Begin 不应被调用")
}

// assertError 返回一个简单的 error 值（用于 fake 注入）。
func assertError(msg string) error {
	return &simpleError{msg: msg}
}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }
