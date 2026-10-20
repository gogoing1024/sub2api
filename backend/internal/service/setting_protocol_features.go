package service

import (
	"context"
	"time"
)

type cachedExcelBPSEnabled struct {
	value     bool
	expiresAt int64 // unix nano
}

// excelBPSEnabledCacheTTL bounds how long another instance keeps serving BPS
// after an administrator disables it; the saving instance invalidates at once.
const excelBPSEnabledCacheTTL = 5 * time.Second

const excelBPSEnabledErrorTTL = time.Second

// GetExcelBPSEnabled reports the database-backed Excel/BPS switch. Schedulers
// consult it for every BPS candidate, so reads are served from a short
// in-process cache instead of the database. A missing or empty value means
// enabled. A failed read keeps the last known value and otherwise fails
// closed.
func (s *SettingService) GetExcelBPSEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	if cached, ok := s.excelBPSEnabledCache.Load().(*cachedExcelBPSEnabled); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, _, _ := s.excelBPSEnabledSF.Do(SettingKeyExcelBPSEnabled, func() (any, error) {
		previous, hasPrevious := s.excelBPSEnabledCache.Load().(*cachedExcelBPSEnabled)
		if hasPrevious && previous != nil && time.Now().UnixNano() < previous.expiresAt {
			return previous.value, nil
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{SettingKeyExcelBPSEnabled})
		if err != nil {
			enabled := hasPrevious && previous != nil && previous.value
			s.excelBPSEnabledCache.Store(&cachedExcelBPSEnabled{value: enabled, expiresAt: time.Now().Add(excelBPSEnabledErrorTTL).UnixNano()})
			return enabled, nil
		}
		value := values[SettingKeyExcelBPSEnabled]
		enabled := value == "" || value == "true"
		s.excelBPSEnabledCache.Store(&cachedExcelBPSEnabled{value: enabled, expiresAt: time.Now().Add(excelBPSEnabledCacheTTL).UnixNano()})
		return enabled, nil
	})
	enabled, _ := result.(bool)
	return enabled
}

// InvalidateExcelBPSEnabledCache makes the next read observe a saved switch.
func (s *SettingService) InvalidateExcelBPSEnabledCache() {
	if s == nil {
		return
	}
	s.excelBPSEnabledSF.Forget(SettingKeyExcelBPSEnabled)
	// Keep the value so a failed re-read still falls back to it.
	expired := &cachedExcelBPSEnabled{}
	if cached, ok := s.excelBPSEnabledCache.Load().(*cachedExcelBPSEnabled); ok && cached != nil {
		expired.value = cached.value
	}
	s.excelBPSEnabledCache.Store(expired)
}
