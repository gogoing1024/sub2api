package service

import "context"

// excelBPSGloballyEnabled guards every BPS route with the admin-facing
// database switch so the protocol can be disabled without a restart.
func (s *OpenAIGatewayService) excelBPSGloballyEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return true
	}
	enabled, err := s.settingService.GetProtocolFeatureEnabled(ctx, SettingKeyExcelBPSEnabled)
	return err == nil && enabled
}
