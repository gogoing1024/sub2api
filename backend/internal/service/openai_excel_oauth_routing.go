package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
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

const excelOAuthRouteUnavailableReason = GatewayFailureReason("excel_oauth_route_unavailable")

// validateExcelOAuthRoute takes the global switch as read by the caller, which
// must route with that same value: a second read could flip to off and send a
// BPS-only account down the native path.
func validateExcelOAuthRoute(account *Account, model string, bpsEnabled bool) error {
	if account.IsBPSOAuth() && (!account.IsExcelBPSEnabledForModel(model) || !bpsEnabled) {
		return errExcelOAuthRouteUnavailable
	}
	return nil
}

// newExcelOAuthRouteFailoverError is the forward-time backstop for a BPS-only
// account that cannot serve this request (the global switch went off under a
// sticky session, or channel mapping changed the model after selection): the
// handler moves on to another account instead of answering 400.
func newExcelOAuthRouteFailoverError() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusBadGateway,
		Scope:             GatewayFailureScopeAccount,
		Reason:            excelOAuthRouteUnavailableReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  http.StatusBadGateway,
		ClientMessage:     errExcelOAuthRouteUnavailable.Error(),
	}
}

// stickyShouldYieldToBPS reports whether a sticky binding to a native account
// must give way to BPS: only when the request's entry point has a BPS route and
// the pool holds another BPS-only account that the caller's scheduler would
// actually pick for this very request. Chat Completions, Anthropic Messages and
// count_tokens keep their native binding (and its prompt cache), and so does
// every request while the BPS candidates are cooling down or otherwise
// unschedulable; evicting then would only move the session to another native
// account.
func stickyShouldYieldToBPS(ctx context.Context, sticky *Account, candidates []Account, requestedModel string, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, schedulable func(*Account) bool) bool {
	if sticky == nil || sticky.IsExcelBPSEnabledForModel(requestedModel) || isBPSUnsupportedInbound(ctx) {
		return false
	}
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.ID != sticky.ID && candidate.IsBPSOAuth() &&
			accountSupportsOpenAICapabilitiesForRequest(ctx, candidate, requestedModel, requiredCapability, requiredImageCapability) &&
			schedulable(candidate) {
			return true
		}
	}
	return false
}
