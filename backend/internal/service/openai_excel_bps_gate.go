package service

import (
	"context"
	"fmt"
	"time"
)

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

// excelBPSGroupPresenceTTL bounds how long a sticky hit trusts a pool listing
// that held no BPS-only account. Any listing of the pool refreshes it.
const excelBPSGroupPresenceTTL = 10 * time.Second

type excelBPSGroupPresenceEntry struct {
	hasBPSOAuth bool
	expiresAt   time.Time
}

func excelBPSGroupPresenceKey(groupID *int64, platform string) string {
	return fmt.Sprintf("%d|%s", derefGroupID(groupID), NormalizeOpenAICompatiblePlatform(platform))
}

// recordExcelBPSGroupPresence notes whether a pool listing holds a BPS-only
// account, before any filtering, so membership rather than momentary
// schedulability decides it.
func (s *OpenAIGatewayService) recordExcelBPSGroupPresence(groupID *int64, platform string, accounts []Account) {
	if s == nil {
		return
	}
	hasBPS := false
	for i := range accounts {
		if accounts[i].IsBPSOAuth() {
			hasBPS = true
			break
		}
	}
	s.excelBPSGroupPresence.Store(excelBPSGroupPresenceKey(groupID, platform), excelBPSGroupPresenceEntry{hasBPSOAuth: hasBPS, expiresAt: time.Now().Add(excelBPSGroupPresenceTTL)})
}

// groupMayHaveBPSOAuth is false only while a recent listing of the pool held
// no BPS-only account. A sticky hit then has nothing to yield to and skips
// listing the pool; without a fresh record it lists as before.
func (s *OpenAIGatewayService) groupMayHaveBPSOAuth(groupID *int64, platform string) bool {
	if s == nil {
		return true
	}
	value, ok := s.excelBPSGroupPresence.Load(excelBPSGroupPresenceKey(groupID, platform))
	if !ok {
		return true
	}
	entry, valid := value.(excelBPSGroupPresenceEntry)
	return !valid || entry.hasBPSOAuth || !time.Now().Before(entry.expiresAt)
}

// isExcelBPSRouteCoolingDown covers the in-memory BPS cooldowns that the
// candidate pool does not filter: the account's BPS 429 cooldown and the
// (account, model) permission cooldown.
func (s *OpenAIGatewayService) isExcelBPSRouteCoolingDown(account *Account, requestedModel string) bool {
	return s.isExcelBPSModelCoolingDown(account, requestedModel) || s.isExcelBPSCoolingDown(account, requestedModel)
}
