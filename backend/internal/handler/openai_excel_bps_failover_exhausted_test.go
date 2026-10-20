package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// When no other account can serve the model either, the client still gets the
// fixed model-permission 403 instead of a generic upstream error.
func TestOpenAIExcelBPSModelNotAllowedFailoverExhausted_Returns403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:        http.StatusForbidden,
		ResponseBody:      []byte(`{"error":{"message":"must-not-leak"}}`),
		Scope:             service.GatewayFailureScopeAccount,
		Reason:            service.ExcelBPSModelNotAllowedReason,
		NextAccountAction: service.NextAccountRetry,
		ClientStatusCode:  http.StatusForbidden,
		ClientMessage:     "This model is not available on the account's Excel BPS endpoint",
	}, false)

	require.Equal(t, http.StatusForbidden, rec.Code)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	errBody, ok := envelope["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "invalid_request_error", errBody["type"])
	require.Equal(t, string(service.ExcelBPSModelNotAllowedReason), errBody["code"])
	require.Equal(t, "This model is not available on the account's Excel BPS endpoint", errBody["message"])
	require.NotContains(t, rec.Body.String(), "must-not-leak")
}
