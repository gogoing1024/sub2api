package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func decodeExcelBPSEnabled(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var resp struct {
		Data struct {
			ExcelBPSEnabled bool `json:"excel_bps_enabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.ExcelBPSEnabled
}

// Saving an unrelated setting must not switch BPS off: the field is a pointer,
// so an omitted value has to fall back to the stored one.
func TestUpdateSettingsKeepsExcelBPSEnabledWhenOmitted(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyExcelBPSEnabled: "true"})

	rec := doUpdateSettings(t, h, map[string]any{"available_channels_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyExcelBPSEnabled])
	require.True(t, decodeExcelBPSEnabled(t, rec))
}

func TestUpdateSettingsAppliesExplicitExcelBPSEnabled(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyExcelBPSEnabled: "true"})

	rec := doUpdateSettings(t, h, map[string]any{"excel_bps_enabled": false}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[service.SettingKeyExcelBPSEnabled])
	require.False(t, decodeExcelBPSEnabled(t, rec))

	rec = doUpdateSettings(t, h, map[string]any{"excel_bps_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyExcelBPSEnabled])
	require.True(t, decodeExcelBPSEnabled(t, rec))
}

func TestGetSettingsReportsExcelBPSEnabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		want   bool
	}{
		{name: "unset defaults on", stored: map[string]string{}, want: true},
		{name: "disabled", stored: map[string]string{service.SettingKeyExcelBPSEnabled: "false"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStepUpSwitchTestHandler(t, tc.stored)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)

			h.GetSettings(c)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.want, decodeExcelBPSEnabled(t, rec))
		})
	}
}
