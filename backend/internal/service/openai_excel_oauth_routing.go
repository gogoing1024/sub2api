package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

// IsBPSOAuth reports a BPS-only OpenAI account: authorized through the official
// Excel client, it can only serve requests through Excel BPS and never native
// Codex. This build has no independent Excel grant for Codex OAuth accounts.
func (a *Account) IsBPSOAuth() bool {
	return a != nil && a.IsOpenAI() && a.Type == AccountTypeBPSOAuth
}

// IsOpenAIOAuthCredentialAccount reports an OpenAI account that holds OAuth
// credentials (access/refresh token, client_id) maintained by the OpenAI token
// lifecycle: Codex OAuth accounts and BPS-only accounts alike.
func (a *Account) IsOpenAIOAuthCredentialAccount() bool {
	return a != nil && a.IsOpenAI() && (a.Type == AccountTypeOAuth || a.Type == AccountTypeBPSOAuth)
}

// excelBPSRouteEligible: only BPS-only parent accounts on a paid plan route
// through BPS.
func excelBPSRouteEligible(account *Account) bool {
	return account.IsBPSOAuth() && !account.IsShadow() &&
		!strings.EqualFold(strings.TrimSpace(account.GetCredential("plan_type")), "free")
}

// validateBPSOAuthCredentials keeps the account type and the OAuth client in
// step: BPS accepts Excel-client tokens only, and an Excel token can never
// serve native Codex.
func validateBPSOAuthCredentials(platform, accountType string, credentials map[string]any) error {
	if platform != PlatformOpenAI {
		if accountType == AccountTypeBPSOAuth {
			return infraerrors.BadRequest("BPS_OAUTH_INVALID", "BPS OAuth accounts must use the OpenAI platform")
		}
		return nil
	}
	clientID, _ := credentials["client_id"].(string)
	isExcel := strings.TrimSpace(clientID) == openai.ExcelClientID
	switch {
	case accountType == AccountTypeBPSOAuth && !isExcel:
		return infraerrors.BadRequest("BPS_OAUTH_INVALID", "BPS OAuth accounts require credentials issued to the official Excel client")
	case accountType == AccountTypeOAuth && isExcel:
		return infraerrors.BadRequest("BPS_OAUTH_INVALID", "Excel OAuth credentials can only be used by a BPS OAuth account")
	}
	return nil
}

type bpsUnsupportedInboundKey struct{}

// WithBPSUnsupportedInbound marks a request whose entry point has no Excel BPS
// route (Chat Completions, Anthropic Messages). Those handlers forward through
// the native Codex protocol, so the scheduler never selects a BPS-only account.
func WithBPSUnsupportedInbound(ctx context.Context) context.Context {
	return context.WithValue(ctx, bpsUnsupportedInboundKey{}, true)
}

func isBPSUnsupportedInbound(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(bpsUnsupportedInboundKey{}).(bool)
	return marked
}

// errBPSUnsupportedInbound is the forward-time backstop of the same rule.
var errBPSUnsupportedInbound = errors.New("BPS OAuth accounts only serve the Responses API")

const bpsUnsupportedInboundReason = GatewayFailureReason("basispoints_inbound_unsupported")

// newBPSUnsupportedInboundFailoverError lets the handler move on to an account
// that can serve the entry point if a BPS-only account was selected anyway.
func newBPSUnsupportedInboundFailoverError() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusBadGateway,
		Scope:             GatewayFailureScopeAccount,
		Reason:            bpsUnsupportedInboundReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  http.StatusBadGateway,
		ClientMessage:     errBPSUnsupportedInbound.Error(),
	}
}

var errExcelOAuthRouteUnavailable = errors.New("an enabled Excel BPS route is required for Excel OAuth credentials and the requested model; native Codex is unavailable")

const excelOAuthRouteErrorCode = "excel_oauth_route_unavailable"

func (s *OpenAIGatewayService) validateExcelOAuthRoute(ctx context.Context, account *Account, model string) error {
	if account.IsBPSOAuth() && (!account.IsExcelBPSEnabledForModel(model) || !s.excelBPSGloballyEnabled(ctx)) {
		return errExcelOAuthRouteUnavailable
	}
	return nil
}

func writeExcelOAuthRouteError(c *gin.Context) error {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request_error", "code": excelOAuthRouteErrorCode,
		"message": errExcelOAuthRouteUnavailable.Error(),
	}})
	return errExcelOAuthRouteUnavailable
}
