package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// R24 P0-1: 000052 迁移合同测试
// 测试必须读取真实 000052 SQL，验证：
//   - ACTIVE 空凭证真实 Provider 会进入禁用分支
//   - MOCK_LOCAL 被排除，且不会被 UPDATE
//   - UPDATE guard 同时允许 NULL 和等于当前 Provider
//   - 存在 ROW_COUNT == 1 检查和零行 fail-closed
//   - 禁止 EXCEPTION WHEN OTHERS THEN NULL
//   - 提取目标 DO block 并检查筛选范围、更新条件和执行顺序
// ============================================================================

// readMigrationSQL052 读取 000052 迁移 SQL 文件内容
func readMigrationSQL052(t *testing.T, filename string) string {
	t.Helper()
	path := filepath.Join(".", filename)
	content, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read migration SQL file: %s", filename)
	return string(content)
}

// extractDOBlock 从 SQL 中提取 DO $$ ... END $$ 块内容
func extractDOBlock(t *testing.T, sqlContent string) string {
	t.Helper()
	startIdx := strings.Index(sqlContent, "DO $$")
	require.NotEqual(t, -1, startIdx, "must find DO block in 000052 SQL")

	endIdx := strings.Index(sqlContent[startIdx:], "END $$;")
	require.NotEqual(t, -1, endIdx, "must find END $$ in 000052 SQL")

	return sqlContent[startIdx : startIdx+endIdx+len("END $$;")]
}

// ── 验证 ACTIVE 空凭证真实 Provider 进入禁用分支 ──

func TestMigration052_EmptyCredentialDisablesProvider(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 验证 SQL 包含对 credential_vault_key IS NULL 的禁用逻辑
	assert.Contains(t, sqlContent, "credential_vault_key IS NULL",
		"000052 must disable providers with NULL credential_vault_key")

	// 验证 trim 后为空也禁用
	assert.Contains(t, sqlContent, "trim(credential_vault_key) = ''",
		"000052 must disable providers with empty (trimmed) credential_vault_key")

	// 验证 entity_id IS NULL 也禁用
	assert.Contains(t, sqlContent, "entity_id IS NULL",
		"000052 must disable providers with NULL entity_id")

	// 验证 adapter_provider_id IS NULL 也禁用
	assert.Contains(t, sqlContent, "adapter_provider_id IS NULL",
		"000052 must disable providers with NULL adapter_provider_id")
}

// ── 验证 MOCK_LOCAL 被排除 ──

func TestMigration052_MockLocalExcluded(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 禁用分支只处理 NEXUS_ISSUING_HTTP_V1
	assert.Contains(t, sqlContent, "adapter_type = 'NEXUS_ISSUING_HTTP_V1'",
		"000052 disable branches must only target NEXUS_ISSUING_HTTP_V1")

	// 禁用分支不包含 MOCK_LOCAL
	// DO block 内的禁用 UPDATE 使用 WHERE id = ip_rec.id，其中 ip_rec 来自 FOR LOOP
	// FOR LOOP 已按 adapter_type = 'NEXUS_ISSUING_HTTP_V1' 筛选，所以这些 UPDATE 是安全的
	lines := strings.Split(sqlContent, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(trimmed, "SET status = 'DISABLED'") {
			// DO block 内的 UPDATE 使用 WHERE id = ip_rec.id，ip_rec 已被 FOR LOOP 筛选
			// 只需验证 DO block 外的顶层 UPDATE 有 adapter_type = 'NEXUS_ISSUING_HTTP_V1'
			contextEnd := i + 10
			if contextEnd > len(lines) {
				contextEnd = len(lines)
			}
			context := strings.Join(lines[i:contextEnd], "\n")
			// 如果 WHERE 子句使用 id = ip_rec.id，则已在 FOR LOOP 中筛选
			if strings.Contains(context, "id = ip_rec.id") {
				continue // DO block 内部，已通过 FOR LOOP 筛选
			}
			// 顶层 UPDATE 必须有 adapter_type = 'NEXUS_ISSUING_HTTP_V1'
			assert.Contains(t, context, "NEXUS_ISSUING_HTTP_V1",
				"top-level DISABLE UPDATE at line %d must be scoped to NEXUS_ISSUING_HTTP_V1", i+1)
		}
	}

	// DO block 的 FOR LOOP 也只查询 NEXUS_ISSUING_HTTP_V1
	doBlock := extractDOBlock(t, sqlContent)
	assert.Contains(t, doBlock, "adapter_type = 'NEXUS_ISSUING_HTTP_V1'",
		"DO block FOR LOOP must only select NEXUS_ISSUING_HTTP_V1 providers")

	// 确保没有任何地方对 MOCK_LOCAL 执行 UPDATE
	assert.NotContains(t, sqlContent, "adapter_type = 'MOCK_LOCAL'",
		"000052 must NOT have any UPDATE targeting MOCK_LOCAL adapter_type")

}

// ── 验证 UPDATE guard 同时允许 NULL 和等于当前 Provider ──

func TestMigration052_UpdateGuardAllowsNullAndCurrentProvider(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 验证 backfill UPDATE 的 WHERE 包含 issuing_provider_id IS NULL OR issuing_provider_id = ip_rec.id
	assert.Contains(t, sqlContent, "issuing_provider_id IS NULL OR issuing_provider_id = ip_rec.id",
		"000052 backfill UPDATE must allow issuing_provider_id IS NULL OR = current provider")
}

// ── 验证 ROW_COUNT == 1 检查和零行 fail-closed ──

func TestMigration052_RowCountCheckAndZeroRowFailClosed(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 验证使用 GET DIAGNOSTICS 获取 ROW_COUNT
	assert.Contains(t, sqlContent, "GET DIAGNOSTICS update_count = ROW_COUNT",
		"000052 must use GET DIAGNOSTICS to get ROW_COUNT")

	// 验证 ROW_COUNT != 1 时禁用 Provider（零行 fail-closed）
	assert.Contains(t, sqlContent, "IF update_count != 1 THEN",
		"000052 must disable Provider when ROW_COUNT != 1 (zero-row fail-closed)")

	// 验证 fail-closed 分支执行 DISABLE
	doBlock := extractDOBlock(t, sqlContent)
	assert.Contains(t, doBlock, "IF update_count != 1 THEN",
		"DO block must contain ROW_COUNT != 1 check")

	// 验证 fail-closed 分支包含 UPDATE issuing_providers SET status = 'DISABLED'
	failClosedSection := doBlock[strings.Index(doBlock, "IF update_count != 1 THEN"):]
	assert.Contains(t, failClosedSection, "status = 'DISABLED'",
		"fail-closed branch must disable Provider")
}

// ── 验证禁止 EXCEPTION WHEN OTHERS THEN NULL ──

func TestMigration052_NoExceptionSwallowing(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	lines := strings.Split(sqlContent, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line, "EXCEPTION WHEN OTHERS THEN NULL") {
			t.Errorf("000052 SQL line %d must NOT contain 'EXCEPTION WHEN OTHERS THEN NULL' error swallowing: %s", i+1, trimmed)
		}
	}
}

// ── 验证 Vault 路径正则与 000051 一致（从 SQL 提取，不复制常量） ──

func TestMigration052_VaultPathRegex_FromSQL(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 提取 000052 SQL 中的正则
	re := regexp.MustCompile(`regex_pattern\s+TEXT\s+:=\s*'([^']+)'`)
	matches := re.FindStringSubmatch(sqlContent)
	require.Len(t, matches, 2, "must find regex_pattern definition in 000052 SQL")
	pattern := matches[1]
	compiled, err := regexp.Compile(pattern)
	require.NoError(t, err, "extracted regex must compile in Go: %s", pattern)

	// 合法路径应匹配
	validPath := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1"
	assert.True(t, compiled.MatchString(validPath), "valid vault path should match")

	// version 0 不应匹配
	invalidPath := "vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/0"
	assert.False(t, compiled.MatchString(invalidPath), "version 0 should NOT match")
}

// ── 验证 DO block 筛选范围：只处理 ACTIVE + NEXUS_ISSUING_HTTP_V1 + 合法路径 ──

func TestMigration052_DOBlockFilterScope(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// FOR LOOP 筛选条件
	assert.Contains(t, doBlock, "status = 'ACTIVE'", "DO block must filter ACTIVE")
	assert.Contains(t, doBlock, "adapter_type = 'NEXUS_ISSUING_HTTP_V1'", "DO block must filter NEXUS_ISSUING_HTTP_V1")
	assert.Contains(t, doBlock, "credential_vault_key IS NOT NULL", "DO block must filter non-null vault key")
	assert.Contains(t, doBlock, "trim(ip.credential_vault_key) != ''", "DO block must filter non-empty vault key")
	assert.Contains(t, doBlock, "entity_id IS NOT NULL", "DO block must filter non-null entity_id")
	assert.Contains(t, doBlock, "adapter_provider_id IS NOT NULL", "DO block must filter non-null adapter_provider_id")
	assert.Contains(t, doBlock, "credential_vault_key ~ regex_pattern", "DO block must filter valid vault path regex")
}

// ── 验证 DO block 更新条件：tenant_id + issuing_provider_id ──

func TestMigration052_DOBlockUpdateConditions(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// backfill UPDATE 设置 issuing_provider_id 和 tenant_id
	assert.Contains(t, doBlock, "SET issuing_provider_id = ip_rec.id",
		"DO block backfill must set issuing_provider_id")
	assert.Contains(t, doBlock, "tenant_id = COALESCE(tenant_id, ce_tenant_id)",
		"DO block backfill must set tenant_id with COALESCE")
}

// ── 验证 DO block 执行顺序：先校验后 backfill ──

func TestMigration052_DOBlockExecutionOrder(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// 所有禁用 UPDATE 必须在 backfill UPDATE 之前
	firstDisableIdx := strings.Index(doBlock, "SET status = 'DISABLED'")
	require.NotEqual(t, -1, firstDisableIdx, "must find disable UPDATE in DO block")

	backfillIdx := strings.Index(doBlock, "SET issuing_provider_id = ip_rec.id")
	require.NotEqual(t, -1, backfillIdx, "must find backfill UPDATE in DO block")

	// 第一个禁用 UPDATE 应在 backfill UPDATE 之前
	// 注意：backfill 之后也有一个 fail-closed 禁用 UPDATE，所以只需验证第一个禁用在 backfill 之前
	assert.Less(t, firstDisableIdx, backfillIdx,
		"first disable UPDATE must appear before backfill UPDATE in DO block")
}

// ── 验证 down migration 为 no-op 且声明不可逆 ──

func TestMigration052_DownIsNoOpAndIrreversible(t *testing.T) {
	t.Parallel()
	downContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.down.sql")

	// down 必须声明数据安全变更不可逆
	assert.Contains(t, downContent, "不可逆",
		"down migration must declare data changes are irreversible")

	// down 不得删除业务列
	assert.NotContains(t, downContent, "DROP COLUMN",
		"down migration must NOT drop business columns")

	// down 不得删除 000050/000051 建立的索引
	assert.NotContains(t, downContent, "DROP INDEX",
		"down migration must NOT drop indexes from 000050/000051")
}

// ── 验证迁移可重复执行（idempotent） ──

func TestMigration052_IdempotentSafe(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")

	// 禁用 UPDATE 使用 WHERE status = 'ACTIVE'，已禁用的不会被重复处理
	// DO block FOR LOOP 也使用 WHERE status = 'ACTIVE'
	// 验证所有 UPDATE issuing_providers 都有 WHERE 条件
	lines := strings.Split(sqlContent, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(trimmed, "UPDATE issuing_providers") {
			// 查找后续 WHERE 子句
			contextEnd := i + 5
			if contextEnd > len(lines) {
				contextEnd = len(lines)
			}
			context := strings.Join(lines[i:contextEnd], "\n")
			assert.Contains(t, context, "WHERE",
				"UPDATE issuing_providers at line %d must have WHERE clause for idempotency", i+1)
		}
	}
}

// ── 验证完整安全条件矩阵 ──

func TestMigration052_SecurityConditionMatrix(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	requiredConditions := []struct {
		keyword string
		desc    string
	}{
		{keyword: "ce_tenant_id IS NULL OR ce_tenant_id != path_tenant_uuid", desc: "entity/path tenant mismatch"},
		{keyword: "NOT FOUND", desc: "vault record not found"},
		{keyword: "vault_key_version != path_version_int", desc: "key_version mismatch"},
		{keyword: "vault_tenant_id IS NOT NULL AND vault_tenant_id != ce_tenant_id", desc: "vault tenant mismatch"},
		{keyword: "vault_credential_type IS NULL OR vault_credential_type != 'API_KEY'", desc: "credential_type must be API_KEY"},
		{keyword: "vault_status IS NULL OR vault_status != 'ACTIVE'", desc: "vault status must be ACTIVE"},
		{keyword: "vault_expires_at IS NOT NULL AND vault_expires_at <= now()", desc: "expires_at must be future"},
		{keyword: "vault_provider_id IS NULL OR vault_provider_id != ip_adapter_provider_id", desc: "provider_id mismatch"},
		{keyword: "vault_issuing_provider_id IS NOT NULL AND vault_issuing_provider_id != ip_rec.id", desc: "issuing_provider_id owned by other"},
		{keyword: "ref_count != 1", desc: "vault record referenced by multiple providers"},
	}

	for _, cond := range requiredConditions {
		t.Run(cond.desc, func(t *testing.T) {
			assert.Contains(t, doBlock, cond.keyword,
				"DO block missing required security condition: %s (keyword: %q)", cond.desc, cond.keyword)
		})
	}
}

// ── 验证 down 中不存在破坏 000050/000051 的操作 ──

func TestMigration052_DownDoesNotBreakPriorMigrations(t *testing.T) {
	t.Parallel()
	downContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.down.sql")

	// 只检查非注释行（注释中可能提到列名作为说明）
	lines := strings.Split(downContent, "\n")
	executableContent := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		executableContent += line + "\n"
	}

	// 不得删除 000051 建立的列（只检查可执行 SQL，不检查注释）
	assert.NotContains(t, executableContent, "DROP COLUMN",
		"down must NOT drop columns (established by 000050/000051)")

	// 不得删除 000051 建立的约束
	assert.NotContains(t, executableContent, "DROP CONSTRAINT",
		"down must NOT drop constraints from 000050/000051")

	// 不得删除 000051 建立的表
	assert.NotContains(t, executableContent, "DROP TABLE",
		"down must NOT drop tables")

	// 不得删除 000051 建立的索引
	assert.NotContains(t, executableContent, "DROP INDEX",
		"down must NOT drop indexes from 000050/000051")
}

// ============================================================================
// R25 P0: key_version 整数溢出保护测试
// 验证超大 key_version 不会导致 integer out of range 中止整个迁移
// ============================================================================

// TestMigration052_PathVersionTextBeforeInt 验证 path_version_text 在 ::int 之前赋值。
// 不得直接对 split_part 结果执行 ::int，必须先提取为 TEXT 再做范围检查。
func TestMigration052_PathVersionTextBeforeInt(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// path_version_text 赋值必须在 path_version_int::int 之前
	textIdx := strings.Index(doBlock, "path_version_text := split_part")
	require.NotEqual(t, -1, textIdx, "must find path_version_text assignment")

	intIdx := strings.Index(doBlock, "path_version_int := path_version_text::int")
	require.NotEqual(t, -1, intIdx, "must find path_version_int assignment from path_version_text")

	assert.Less(t, textIdx, intIdx,
		"path_version_text must be assigned before path_version_int::int conversion")

	// 不得存在直接对 split_part 执行 ::int 的旧代码
	assert.NotContains(t, doBlock, "split_part(stripped_path, '/', 4)::int",
		"must NOT directly cast split_part to int — must go through path_version_text first")
}

// TestMigration052_IntOverflowGuard_2147483647 验证存在 PostgreSQL INT 上限 2147483647 的检查。
func TestMigration052_IntOverflowGuard_2147483647(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	assert.Contains(t, doBlock, "2147483647",
		"DO block must reference PostgreSQL INT upper bound 2147483647 in overflow guard")
}

// TestMigration052_OverflowLengthGT10_Disables 验证长度大于10位进入禁用分支。
func TestMigration052_OverflowLengthGT10_Disables(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	assert.Contains(t, doBlock, "length(path_version_text) > 10",
		"DO block must check length > 10 for overflow guard")
}

// TestMigration052_OverflowLength10GTBoundary_Disables 验证10位但大于2147483647进入禁用分支。
func TestMigration052_OverflowLength10GTBoundary_Disables(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	assert.Contains(t, doBlock, "length(path_version_text) = 10 AND path_version_text > '2147483647'",
		"DO block must check 10-digit value > '2147483647' for overflow guard")
}

// TestMigration052_LegalBoundary2147483647_AllowsConversion 验证合法边界2147483647允许转换。
// 即 2147483647 不应被溢出检查拦截，应能正常执行 ::int。
func TestMigration052_LegalBoundary2147483647_AllowsConversion(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// 溢出检查条件：length > 10 OR (length = 10 AND > '2147483647')
	// 2147483647 是 10 位且等于 '2147483647'，不满足 > '2147483647'，所以不会被拦截
	// 验证条件使用的是 > 而不是 >=
	assert.Contains(t, doBlock, "path_version_text > '2147483647'",
		"overflow guard must use > (strict greater than), not >=, so 2147483647 passes")
	assert.NotContains(t, doBlock, "path_version_text >= '2147483647'",
		"overflow guard must NOT use >=, which would incorrectly reject 2147483647")
}

// TestMigration052_Overflow2147483648_NotConvertedBeforeRangeCheck 验证2147483648不得在范围检查前转换。
// 即 ::int 必须在溢出检查之后，不能在此之前执行。
func TestMigration052_Overflow2147483648_NotConvertedBeforeRangeCheck(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// 找到溢出检查的位置
	overflowCheckIdx := strings.Index(doBlock, "length(path_version_text) > 10")
	require.NotEqual(t, -1, overflowCheckIdx, "must find overflow check")

	// 找到 ::int 转换的位置
	intConversionIdx := strings.Index(doBlock, "path_version_int := path_version_text::int")
	require.NotEqual(t, -1, intConversionIdx, "must find ::int conversion")

	// ::int 转换必须在溢出检查之后
	assert.Greater(t, intConversionIdx, overflowCheckIdx,
		"path_version_text::int must appear AFTER overflow check, not before")
}

// TestMigration052_OverflowPath_DisablesAndContinues 验证溢出路径执行 DISABLED+UNSUPPORTED+CONTINUE。
func TestMigration052_OverflowPath_DisablesAndContinues(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// 提取溢出检查后的代码段
	overflowIdx := strings.Index(doBlock, "length(path_version_text) > 10")
	require.NotEqual(t, -1, overflowIdx, "must find overflow check")

	// 从溢出检查开始截取到 path_version_int 赋值为止
	intIdx := strings.Index(doBlock, "path_version_int := path_version_text::int")
	require.NotEqual(t, -1, intIdx, "must find ::int conversion")

	overflowSection := doBlock[overflowIdx:intIdx]

	assert.Contains(t, overflowSection, "status = 'DISABLED'",
		"overflow path must set status = DISABLED")
	assert.Contains(t, overflowSection, "adapter_type = 'UNSUPPORTED'",
		"overflow path must set adapter_type = UNSUPPORTED")
	assert.Contains(t, overflowSection, "CONTINUE",
		"overflow path must CONTINUE to next iteration")
}

// TestMigration052_OverflowGuardOrder 验证范围判断、禁用分支和 ::int 的相对执行顺序。
// 顺序必须是：1.提取text → 2.范围检查 → 3.禁用+CONTINUE → 4.::int
func TestMigration052_OverflowGuardOrder(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL052(t, "000052_issuing_ownership_backfill_hardening.up.sql")
	doBlock := extractDOBlock(t, sqlContent)

	// 1. path_version_text 赋值
	textAssignIdx := strings.Index(doBlock, "path_version_text := split_part")
	require.NotEqual(t, -1, textAssignIdx, "must find path_version_text assignment")

	// 2. 溢出检查
	overflowCheckIdx := strings.Index(doBlock, "length(path_version_text) > 10")
	require.NotEqual(t, -1, overflowCheckIdx, "must find overflow check")

	// 3. 禁用分支中的 CONTINUE（溢出路径）
	// 从溢出检查开始找第一个 CONTINUE
	afterOverflow := doBlock[overflowCheckIdx:]
	continueIdx := strings.Index(afterOverflow, "CONTINUE")
	require.NotEqual(t, -1, continueIdx, "must find CONTINUE in overflow path")
	continueIdx += overflowCheckIdx

	// 4. ::int 转换
	intIdx := strings.Index(doBlock, "path_version_int := path_version_text::int")
	require.NotEqual(t, -1, intIdx, "must find ::int conversion")

	// 验证顺序：text赋值 < 溢出检查 < CONTINUE < ::int
	assert.Less(t, textAssignIdx, overflowCheckIdx,
		"step 1: path_version_text assignment must come before overflow check")
	assert.Less(t, overflowCheckIdx, continueIdx,
		"step 2: overflow check must come before CONTINUE in disable branch")
	assert.Less(t, continueIdx, intIdx,
		"step 3: CONTINUE must come before ::int conversion (overflow path skips ::int)")
}
