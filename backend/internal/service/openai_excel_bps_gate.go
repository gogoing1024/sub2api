package service

import (
	"fmt"
	"time"
)

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
