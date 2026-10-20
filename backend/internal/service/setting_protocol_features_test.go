package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type excelBPSSwitchReadRepo struct {
	SettingRepository
	value  *string
	err    error
	reads  int
	stored map[string]string
}

func (r *excelBPSSwitchReadRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	if r.value != nil {
		out[SettingKeyExcelBPSEnabled] = *r.value
	}
	return out, nil
}

func TestGetExcelBPSEnabledDefaultsAndCaches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *string
		want  bool
	}{
		{name: "missing", want: true},
		{name: "empty", value: new(string), want: true},
		{name: "true", value: func() *string { v := "true"; return &v }(), want: true},
		{name: "false", value: func() *string { v := "false"; return &v }(), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &excelBPSSwitchReadRepo{value: tc.value}
			svc := NewSettingService(repo, &config.Config{})
			for i := 0; i < 5; i++ {
				require.Equal(t, tc.want, svc.GetExcelBPSEnabled(context.Background()))
			}
			require.Equal(t, 1, repo.reads)
		})
	}
}

func TestGetExcelBPSEnabledInvalidationAndErrors(t *testing.T) {
	on, off := "true", "false"
	repo := &excelBPSSwitchReadRepo{value: &on}
	svc := NewSettingService(repo, &config.Config{})
	require.True(t, svc.GetExcelBPSEnabled(context.Background()))

	// A saved change applies at once on this instance.
	repo.value = &off
	svc.InvalidateExcelBPSEnabledCache()
	require.False(t, svc.GetExcelBPSEnabled(context.Background()))

	// A failed read keeps the last known value instead of flipping the switch.
	repo.value = &on
	svc.InvalidateExcelBPSEnabledCache()
	require.True(t, svc.GetExcelBPSEnabled(context.Background()))
	repo.err = errors.New("db down")
	svc.InvalidateExcelBPSEnabledCache()
	require.True(t, svc.GetExcelBPSEnabled(context.Background()))

	// Without a known value a failed read fails closed.
	fresh := NewSettingService(&excelBPSSwitchReadRepo{err: errors.New("db down")}, &config.Config{})
	require.False(t, fresh.GetExcelBPSEnabled(context.Background()))
}
