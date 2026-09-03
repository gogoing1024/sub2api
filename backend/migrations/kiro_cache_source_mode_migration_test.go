package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKiroCacheSourceModeMigration(t *testing.T) {
	content, err := FS.ReadFile("232_kiro_cache_source_mode.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// 新列默认 emulation_only：存量分组行为不变。
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS kiro_cache_source_mode VARCHAR(24) NOT NULL DEFAULT 'emulation_only'")

	// CHECK 限定两个取值，且带幂等守卫（重复执行不报错）。
	require.Contains(t, sql, "groups_kiro_cache_source_mode_check")
	require.Contains(t, sql, "CHECK (kiro_cache_source_mode IN ('emulation_only', 'upstream_first'))")
	require.Contains(t, sql, "IF NOT EXISTS ( SELECT 1 FROM pg_constraint c")

	require.Contains(t, sql, "COMMENT ON COLUMN groups.kiro_cache_source_mode IS")
}
