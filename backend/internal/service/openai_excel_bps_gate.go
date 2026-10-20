package service

import "context"

// excelBPSGloballyEnabled guards every BPS route with the admin-facing
// database switch so the protocol can be disabled without a restart. The
// value comes from a short in-process cache (SettingService.GetExcelBPSEnabled);
// a request that both checks and acts on the switch must read it once.
func (s *OpenAIGatewayService) excelBPSGloballyEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return true
	}
	return s.settingService.GetExcelBPSEnabled(ctx)
}

// filterBPSOAuthAccountsWhenDisabled drops BPS-only accounts from a candidate
// pool while the global switch is off, so both schedulers (and the sticky
// eviction that consults the pool) treat them as unschedulable. The switch is
// read only when the pool actually holds a BPS-only account.
func (s *OpenAIGatewayService) filterBPSOAuthAccountsWhenDisabled(ctx context.Context, accounts []Account) []Account {
	hasBPS := false
	for i := range accounts {
		if accounts[i].IsBPSOAuth() {
			hasBPS = true
			break
		}
	}
	if !hasBPS || s.excelBPSGloballyEnabled(ctx) {
		return accounts
	}
	kept := make([]Account, 0, len(accounts))
	for i := range accounts {
		if !accounts[i].IsBPSOAuth() {
			kept = append(kept, accounts[i])
		}
	}
	return kept
}

// isExcelBPSRouteCoolingDown covers the in-memory BPS cooldowns that the
// candidate pool does not filter: the account's BPS 429 cooldown and the
// (account, model) permission cooldown.
func (s *OpenAIGatewayService) isExcelBPSRouteCoolingDown(account *Account, requestedModel string) bool {
	return s.isExcelBPSModelCoolingDown(account, requestedModel) || s.isExcelBPSCoolingDown(account, requestedModel)
}
