package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func codexOAuthAccountWithBPSFlag() *Account {
	account := excelAccount()
	delete(account.Credentials, "client_id")
	return account
}

// BPS only accepts Excel-client tokens and this build has no independent Excel
// grant, so the BPS flag on a Codex OAuth account must not route anything.
func TestIsExcelBPSEnabledRequiresExcelOAuth(t *testing.T) {
	require.True(t, excelAccount().IsExcelBPSEnabled())
	require.True(t, excelAccount().IsExcelBPSEnabledForModel("gpt-5.6-sol"))

	codex := codexOAuthAccountWithBPSFlag()
	require.False(t, codex.IsExcelBPSEnabled())
	require.False(t, codex.IsExcelBPSEnabledForModel("gpt-5.6-sol"))

	codex.Credentials["client_id"] = "app_codex_client"
	require.False(t, codex.IsExcelBPSEnabled())
}

// 403 auto-recovery must never switch BPS back on for a Codex OAuth account.
func TestExcelBPS403RecoveryIgnoresCodexOAuthAccounts(t *testing.T) {
	disabledAt := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	pending := func(account *Account) *Account {
		account.Extra["openai_excel_bps"] = false
		account.Extra["openai_excel_bps_auto_disable_on_403"] = true
		account.Extra[ExcelBPSAutoRecoverOn403Key] = true
		account.Extra[ExcelBPS403DisabledAtKey] = disabledAt
		return account
	}
	require.True(t, pending(excelAccount()).IsExcelBPS403RecoveryPending())
	require.False(t, pending(codexOAuthAccountWithBPSFlag()).IsExcelBPS403RecoveryPending())
}

func TestValidateExcelBPSGroupPolicyRejectsBPSOnCodexOAuth(t *testing.T) {
	svc := &adminServiceImpl{}

	err := svc.validateExcelBPSGroupPolicy(context.Background(), codexOAuthAccountWithBPSFlag())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Excel OAuth")

	require.NoError(t, svc.validateExcelBPSGroupPolicy(context.Background(), excelAccount()))

	off := codexOAuthAccountWithBPSFlag()
	off.Extra["openai_excel_bps"] = false
	require.NoError(t, svc.validateExcelBPSGroupPolicy(context.Background(), off), "turning BPS off stays allowed on any account")

	excel := excelAccount()
	excel.Credentials["client_id"] = openai.ExcelClientID
	excel.Credentials["plan_type"] = "free"
	require.Error(t, svc.validateExcelBPSGroupPolicy(context.Background(), excel), "free plans stay ineligible")
}
