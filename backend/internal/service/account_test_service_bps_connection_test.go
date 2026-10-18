package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newBPSAccountTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/300/test", nil).WithContext(context.Background())
	return c, rec
}

func excelBPSDisabledSettingService() *SettingService {
	return NewSettingService(&excelBPSSwitchSettingRepo{values: map[string]string{SettingKeyExcelBPSEnabled: "false"}}, &config.Config{})
}

// The default account test for a BPS-enabled account must take the gateway
// BPS route instead of the hard-coded native Codex probe.
func TestAccountTestServiceRoutesBPSAccountThroughBPS(t *testing.T) {
	wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := &AccountTestService{openaiGatewayService: openAIClientToolsTestService(upstream)}
	c, rec := newBPSAccountTestContext()

	err := svc.testOpenAIAccountConnection(c, excelAccount(), "gpt-5.6-sol", "", "")

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, HTTPUpstreamProfileExcelBPS, HTTPUpstreamProfileFromContext(upstream.requests[0].Context()))
	require.Contains(t, rec.Body.String(), "test_complete")
	require.NotContains(t, rec.Body.String(), `"type":"error"`)
}

// Excel OAuth tokens only work on BPS; with BPS globally off the test must
// fail closed instead of sending the Excel token to ChatGPT Codex.
func TestAccountTestServiceExcelOAuthRequiresBPSRoute(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	gateway := openAIClientToolsTestService(upstream)
	gateway.settingService = excelBPSDisabledSettingService()
	svc := &AccountTestService{openaiGatewayService: gateway, httpUpstream: upstream}
	account := excelAccount()
	account.Credentials["client_id"] = openai.ExcelClientID
	c, rec := newBPSAccountTestContext()

	_ = svc.testOpenAIAccountConnection(c, account, "gpt-5.6-sol", "", "")

	require.Empty(t, upstream.requests)
	require.Contains(t, rec.Body.String(), errExcelOAuthRouteUnavailable.Error())
}

func TestAccountTestServiceBPSToolsModeHonoursGlobalSwitch(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	gateway := openAIClientToolsTestService(upstream)
	gateway.settingService = excelBPSDisabledSettingService()
	svc := &AccountTestService{openaiGatewayService: gateway, httpUpstream: upstream}
	c, rec := newBPSAccountTestContext()
	// A Codex OAuth account skips the Excel OAuth guard and reaches the probe.
	account := excelAccount()
	delete(account.Credentials, "client_id")

	_ = svc.testOpenAIAccountConnection(c, account, "gpt-5.6-sol", "", AccountTestModeBPSTools)

	require.Empty(t, upstream.requests)
	require.Contains(t, rec.Body.String(), "Excel BPS is disabled globally")
}

// A failing BPS test must show the BPS status and its own (redacted) message
// in the dialog instead of only basispoints_upstream_error.
func TestAccountTestServiceShowsBPSRejectionStatusAndMessage(t *testing.T) {
	body := `{"error":{"code":"token_invalidated","message":"Bearer test-token was rejected for this workspace"}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_bps_401"}}, Body: io.NopCloser(strings.NewReader(body))}}
	svc := &AccountTestService{openaiGatewayService: openAIClientToolsTestService(upstream)}
	c, rec := newBPSAccountTestContext()

	_ = svc.testOpenAIAccountConnection(c, excelAccount(), "gpt-5.6-sol", "", "")

	out := rec.Body.String()
	require.Len(t, upstream.requests, 1)
	require.Contains(t, out, `"type":"upstream_response"`)
	require.Contains(t, out, `"upstream_status":401`)
	require.Contains(t, out, `"request_id":"req_bps_401"`)
	require.Contains(t, out, `"upstream_error_code":"token_invalidated"`)
	require.Contains(t, out, `"upstream_error_message":`)
	require.Contains(t, out, "was rejected for this workspace")
	require.NotContains(t, out, "test-token", "the request token must never reach the test output")
	require.Contains(t, out, "(HTTP 401)")
}
