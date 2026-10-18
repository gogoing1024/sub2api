package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type excelBPSSwitchSettingRepo struct {
	SettingRepository
	values map[string]string
}

func (r *excelBPSSwitchSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

type excelBPSRecoveryScanRepo struct {
	AccountRepository
	scans atomic.Int32
}

func (r *excelBPSRecoveryScanRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	r.scans.Add(1)
	return nil, nil
}

func TestSyncExcelBPS403RecoverySkipsScanWhenGloballyDisabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		values    map[string]string
		wantScans int32
	}{
		{name: "disabled", values: map[string]string{SettingKeyExcelBPSEnabled: "false"}, wantScans: 0},
		{name: "enabled", values: map[string]string{SettingKeyExcelBPSEnabled: "true"}, wantScans: 1},
		{name: "unset defaults on", values: map[string]string{}, wantScans: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &excelBPSRecoveryScanRepo{}
			svc := &OpenAIGatewayService{
				accountRepo:    repo,
				settingService: NewSettingService(&excelBPSSwitchSettingRepo{values: tc.values}, &config.Config{}),
			}
			svc.syncExcelBPS403Recovery(context.Background())
			require.Equal(t, tc.wantScans, repo.scans.Load())
		})
	}
}
