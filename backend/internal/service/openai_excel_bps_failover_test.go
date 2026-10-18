package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func forwardExcelBPSWithStatus(t *testing.T, status int, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}
	svc := openAIClientToolsTestService(upstream)
	request, err := buildExcelBPSAccountTestBody("gpt-5.6-sol", "hi")
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(request))
	_, err = svc.Forward(context.Background(), c, excelAccount(), request)
	require.Len(t, upstream.requests, 1)
	return rec, err
}

// A BPS-only account has no other channel: a generic BPS 401/403 disqualifies
// the whole account, so the request moves on to another account without any
// BPS text reaching the client.
func TestExcelBPSAccountRejectionFailsOverToAnotherAccount(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		rec, err := forwardExcelBPSWithStatus(t, status, `{"error":{"code":"account_blocked","message":"private upstream diagnostic"}}`)

		var failover *UpstreamFailoverError
		require.True(t, errors.As(err, &failover), "status %d", status)
		require.Equal(t, ExcelBPSAccountRejectedReason, failover.Reason)
		require.Equal(t, status, failover.StatusCode)
		require.True(t, failover.ShouldRetryNextAccount())
		require.True(t, failover.IsCredentialFailure())
		require.Empty(t, failover.ResponseBody)
		require.NotContains(t, failover.ClientMessage, "private")
		require.False(t, rec.Body.Len() > 0, "leave the response to the handler")
	}
}

// A model-permission 403 only cools that model; it is not an account rejection.
func TestExcelBPSModelPermission403DoesNotFailOver(t *testing.T) {
	_, err := forwardExcelBPSWithStatus(t, http.StatusForbidden, `{"error":{"code":"model_not_allowed","message":"model unavailable"}}`)

	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Error(t, err)
}
