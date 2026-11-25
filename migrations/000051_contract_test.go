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
// R23 Item 6: 强化迁移合同测试
// 测试必须读取真实 000051 SQL，验证：
//   - 列存在保障位于第一次 adapter_provider_id 数据引用之前
//   - TEXT、VARCHAR、UUID 分支均存在
//   - UUID 清理发生在 FK 重建之前
//   - 从 SQL 提取真实 vault 正则后测试合法/非法路径（禁止复制正则常量）
//   - 版本 0 不匹配
//   - credential_type/status/expires_at/provider_id/已有 issuing_provider_id 条件
//   - down 中索引和约束先于相关列删除
// ============================================================================

// readMigrationSQL 读取迁移 SQL 文件内容
func readMigrationSQL(t *testing.T, filename string) string {
	t.Helper()
	path := filepath.Join(".", filename)
	content, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read migration SQL file: %s", filename)
	return string(content)
}

// extractVaultRegexFromSQL 从 SQL 内容中提取真实 vault 路径正则表达式
func extractVaultRegexFromSQL(t *testing.T, sqlContent string) *regexp.Regexp {
	t.Helper()
	// SQL 中正则模式格式: regex_pattern TEXT := '^vault://...$';
	// 需要提取单引号内的正则字符串
	re := regexp.MustCompile(`regex_pattern\s+TEXT\s+:=\s*'([^']+)'`)
	matches := re.FindStringSubmatch(sqlContent)
	require.Len(t, matches, 2, "must find regex_pattern definition in SQL")
	// SQL 正则中 [0-9a-fA-F] 等不需要转义，但 Go regexp 需要确认兼容性
	// PostgreSQL 正则与 Go regexp 都支持基本 POSIX 语法
	pattern := matches[1]
	compiled, err := regexp.Compile(pattern)
	require.NoError(t, err, "extracted regex must compile in Go: %s", pattern)
	return compiled
}

// ── 从 SQL 提取真实 vault 正则后测试合法/非法路径 ──

func TestVaultPathRegex_FromSQL_ValidPaths(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")
	vaultRegex := extractVaultRegexFromSQL(t, sqlContent)

	validPaths := []string{
		"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1",
		"vault://abcdef01-2345-6789-abcd-ef0123456789/01234567-89ab-cdef-0123-456789abcdef/api_key/42",
		"vault://ABCDEF01-2345-6789-ABCD-EF0123456789/01234567-89AB-CDEF-0123-456789ABCDEF/api_key/999",
	}

	for _, path := range validPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			assert.True(t, vaultRegex.MatchString(path),
				"valid vault path should match SQL regex: %s", path)
		})
	}
}

func TestVaultPathRegex_FromSQL_InvalidPaths(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")
	vaultRegex := extractVaultRegexFromSQL(t, sqlContent)

	invalidPaths := []struct {
		path string
		desc string
	}{
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/0", "version 0 must be rejected"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/abc", "version must be numeric"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/secret_key/1", "credential_type must be api_key"},
		{"vault://not-a-uuid/660e8400-e29b-41d4-a716-446655440001/api_key/1", "tenant_uuid must be valid UUID"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/not-a-uuid/api_key/1", "record_uuid must be valid UUID"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1/extra", "extra trailing segment"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/", "empty version"},
		{"vault://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key", "missing version"},
		{"http://550e8400-e29b-41d4-a716-446655440000/660e8400-e29b-41d4-a716-446655440001/api_key/1", "wrong protocol"},
		{"", "empty string"},
	}

	for _, tc := range invalidPaths {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			assert.False(t, vaultRegex.MatchString(tc.path),
				"invalid vault path should NOT match SQL regex: %s (desc: %s)", tc.path, tc.desc)
		})
	}
}

// ── 验证列存在保障位于第一次 adapter_provider_id 数据引用之前 ──

func TestMigrationFile_ColumnExistenceBeforeDataReference(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	// 查找列存在保障（DO $$ ... ADD COLUMN ... END $$）的位置
	// 第 3 节：确保 issuing_providers.adapter_provider_id 列存在

	colEnsureIdx := strings.Index(sqlContent, "R21 Step D: 确保 issuing_providers.adapter_provider_id 列存在")
	require.NotEqual(t, -1, colEnsureIdx, "column existence guarantee section must exist")

	// 查找第一次引用 adapter_provider_id 的数据操作（UPDATE ... WHERE adapter_provider_id）
	// 排除注释行和 ADD COLUMN 语句
	lines := strings.Split(sqlContent, "\n")
	firstDataRefIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		// 数据引用 = WHERE 子句中引用 adapter_provider_id（不是 ADD COLUMN 或 ALTER COLUMN）
		if strings.Contains(trimmed, "adapter_provider_id") &&
			!strings.Contains(trimmed, "ADD COLUMN") &&
			!strings.Contains(trimmed, "ALTER COLUMN") &&
			!strings.Contains(trimmed, "DROP CONSTRAINT") &&
			!strings.Contains(trimmed, "ADD CONSTRAINT") &&
			!strings.Contains(trimmed, "RENAME COLUMN") &&
			!strings.Contains(trimmed, "DROP COLUMN") &&
			!strings.Contains(trimmed, "CREATE INDEX") &&
			!strings.Contains(trimmed, "col_type") &&
			!strings.Contains(trimmed, "information_schema") {
			// 找到 WHERE 子句中的 adapter_provider_id 引用
			if strings.Contains(trimmed, "WHERE") || strings.Contains(trimmed, "UPDATE") {
				firstDataRefIdx = i
				break
			}
		}
	}

	require.NotEqual(t, -1, firstDataRefIdx, "must find first data reference to adapter_provider_id")

	// 列存在保障的行号
	colEnsureLineIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "R21 Step D: 确保 issuing_providers.adapter_provider_id 列存在") {
			colEnsureLineIdx = i
			break
		}
	}
	require.NotEqual(t, -1, colEnsureLineIdx)

	// 验证列存在保障出现在第一次数据引用之前
	assert.Less(t, colEnsureLineIdx, firstDataRefIdx,
		"column existence guarantee (line %d) must appear before first data reference (line %d)",
		colEnsureLineIdx+1, firstDataRefIdx+1)
}

// ── 验证 TEXT、VARCHAR、UUID 分支均存在 ──

func TestMigrationFile_ThreeTypeBranchesExist(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	// 验证 TEXT 分支
	assert.Contains(t, sqlContent, "col_type = 'text'",
		"SQL must have TEXT branch for adapter_provider_id type conversion")

	// 验证 VARCHAR 分支
	assert.Contains(t, sqlContent, "col_type = 'character varying'",
		"SQL must have VARCHAR branch for adapter_provider_id type conversion")

	// 验证 UUID 分支
	assert.Contains(t, sqlContent, "col_type = 'uuid'",
		"SQL must have UUID branch for adapter_provider_id cleanup")
}

// ── 验证 UUID 清理发生在 FK 重建之前 ──

func TestMigrationFile_UUIDCleanupBeforeFKRebuild(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	// 在 UUID 分支中，查找清理 UPDATE 和 FK 重建的相对位置
	uuidBranchStart := strings.Index(sqlContent, "col_type = 'uuid'")
	require.NotEqual(t, -1, uuidBranchStart, "UUID branch must exist")

	// 在 UUID 分支之后查找清理悬空引用的 UPDATE
	uuidSection := sqlContent[uuidBranchStart:]

	// 清理 UPDATE：先禁用 provider，再清空 adapter_provider_id
	cleanupUpdateIdx := strings.Index(uuidSection, "SET adapter_provider_id = NULL")
	require.NotEqual(t, -1, cleanupUpdateIdx, "UUID branch must have cleanup UPDATE")

	// FK 重建：ADD CONSTRAINT ... FOREIGN KEY
	fkRebuildIdx := strings.Index(uuidSection, "FOREIGN KEY (adapter_provider_id) REFERENCES adapter_provider(id)")
	require.NotEqual(t, -1, fkRebuildIdx, "UUID branch must have FK rebuild")

	// 验证清理在 FK 重建之前
	assert.Less(t, cleanupUpdateIdx, fkRebuildIdx,
		"UUID cleanup UPDATE must appear before FK rebuild in UUID branch")
}

// ── 验证 backfill 中的安全条件 ──

func TestMigrationFile_BackfillSecurityConditions(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	requiredConditions := []struct {
		keyword string
		desc    string
	}{
		// 原有条件
		{keyword: `!~ regex_pattern`, desc: "strict regex validation for vault path"},
		{keyword: `ce_tenant_id != path_tenant_uuid`, desc: "path tenant must match corporate_entity tenant_id"},
		{keyword: `vault_key_version != path_version_int`, desc: "path version must match vault key_version"},
		{keyword: `vault_tenant_id IS NOT NULL AND vault_tenant_id != ce_tenant_id`, desc: "vault tenant_id must be empty or match entity tenant"},
		{keyword: `ref_count != 1`, desc: "unique reference check for vault record"},
		{keyword: `issuing_provider_id IS NULL`, desc: "idempotent backfill guard"},

		// R23 Item 2 新增条件
		{keyword: `vault_credential_type != 'API_KEY'`, desc: "credential_type must be API_KEY"},
		{keyword: `vault_status != 'ACTIVE'`, desc: "vault status must be ACTIVE"},
		{keyword: `vault_expires_at <= now()`, desc: "expires_at must be empty or future"},
		{keyword: `vault_provider_id != ip_adapter_provider_id`, desc: "vault provider_id must match adapter_provider_id"},
		{keyword: `vault_issuing_provider_id != ip_rec.id`, desc: "existing issuing_provider_id must be empty or match current provider"},
		{keyword: `[1-9][0-9]*`, desc: "version regex rejects 0"},
	}

	for _, cond := range requiredConditions {
		t.Run(cond.desc, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, sqlContent, cond.keyword,
				"SQL file missing required backfill security condition: %s (keyword: %q)", cond.desc, cond.keyword)
		})
	}
}

// ── 验证 down 中索引和约束先于相关列删除 ──

func TestMigrationFile_DownOrder_IndexBeforeColumnDrop(t *testing.T) {
	t.Parallel()
	downContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.down.sql")

	lines := strings.Split(downContent, "\n")

	// 查找索引删除位置
	dropIndexIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "DROP INDEX IF EXISTS idx_adapter_credential_vault_issuing_provider") {
			dropIndexIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dropIndexIdx, "down migration must drop index")

	// 查找列删除位置
	dropColumnIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "DROP COLUMN IF EXISTS issuing_provider_id") {
			dropColumnIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dropColumnIdx, "down migration must drop column")

	// 索引删除必须在列删除之前
	assert.Less(t, dropIndexIdx, dropColumnIdx,
		"down migration: index drop (line %d) must appear before column drop (line %d)",
		dropIndexIdx+1, dropColumnIdx+1)
}

func TestMigrationFile_DownOrder_ConstraintBeforeColumnDrop(t *testing.T) {
	t.Parallel()
	downContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.down.sql")

	lines := strings.Split(downContent, "\n")

	// 查找 CHECK 约束删除位置
	dropCheckIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "DROP CONSTRAINT IF EXISTS chk_issuing_adapter_provider_id") {
			dropCheckIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dropCheckIdx, "down migration must drop CHECK constraint")

	// 查找 FK 约束删除位置
	dropFKIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "DROP CONSTRAINT IF EXISTS issuing_providers_adapter_provider_id_fkey") {
			dropFKIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dropFKIdx, "down migration must drop FK constraint")

	// 查找列删除位置（adapter_credential_vault.issuing_provider_id）
	dropColumnIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "DROP COLUMN IF EXISTS issuing_provider_id") {
			dropColumnIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dropColumnIdx)

	// CHECK 和 FK 约束删除必须在列删除之前
	assert.Less(t, dropCheckIdx, dropColumnIdx,
		"down migration: CHECK constraint drop must appear before column drop")
	assert.Less(t, dropFKIdx, dropColumnIdx,
		"down migration: FK constraint drop must appear before column drop")
}

// ── 验证无 EXCEPTION WHEN OTHERS THEN NULL ──

func TestMigrationFile_NoExceptionSwallowing(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	lines := strings.Split(sqlContent, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line, "EXCEPTION WHEN OTHERS THEN NULL") {
			t.Errorf("SQL file line %d must NOT contain 'EXCEPTION WHEN OTHERS THEN NULL' error swallowing: %s", i+1, trimmed)
		}
	}
}

// ── 验证 adapter_type CHECK 约束包含 CARD_ISSUER ──

func TestMigrationFile_AdapterTypeCheckIncludesCardIssuer(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	assert.Contains(t, sqlContent, "'CARD_ISSUER'",
		"adapter_provider CHECK constraint must include CARD_ISSUER")
}

// ── 验证 issuing_providers CHECK 约束包含三种 adapter_type ──

func TestMigrationFile_IssuingAdapterTypeCheckIncludesAllTypes(t *testing.T) {
	t.Parallel()
	sqlContent := readMigrationSQL(t, "000051_issuing_freeze_final_corrections.up.sql")

	assert.Contains(t, sqlContent, "'NEXUS_ISSUING_HTTP_V1', 'MOCK_LOCAL', 'UNSUPPORTED'",
		"issuing_providers CHECK constraint must include all three adapter types")
}
