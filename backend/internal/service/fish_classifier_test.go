package service

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

func TestClassifyFishErrorAuth(t *testing.T) {
	failure := classifyFishError(&fish.AuthError{RequestError: fish.RequestError{Message: "Invalid token", StatusCode: 401}})
	if failure.Failover == nil || failure.Failover.Reason != fishFailureAuth {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if !failure.SetAccountError {
		t.Fatal("auth must set account error (fish token has no refresh)")
	}
	if failure.Cooldown > 0 {
		t.Fatal("auth should not cooldown")
	}
}

func TestClassifyFishErrorInsufficientCredits(t *testing.T) {
	failure := classifyFishError(&fish.InsufficientCreditsError{RequestError: fish.RequestError{Message: "insufficient credits", StatusCode: 402}})
	if failure.Failover == nil || failure.Failover.Reason != fishFailureInsufficientCredits {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Cooldown != 2*time.Hour {
		t.Fatalf("cooldown = %v, want 2h (quota class)", failure.Cooldown)
	}
	if failure.Failover.NextAccountAction != NextAccountRetry {
		t.Fatal("insufficient credits should retry next account")
	}
}

func TestClassifyFishErrorRateLimited(t *testing.T) {
	failure := classifyFishError(&fish.RateLimitedError{RequestError: fish.RequestError{StatusCode: 429}})
	if failure.Cooldown != fishRateLimitCooldown || failure.Failover.Reason != fishFailureUpstream {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestClassifyFishErrorTemporary(t *testing.T) {
	failure := classifyFishError(&fish.UpstreamTemporaryError{RequestError: fish.RequestError{StatusCode: 503}})
	if failure.Failover == nil || failure.Failover.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.NextAccountAction != NextAccountRetry || failure.Cooldown > 0 {
		t.Fatalf("temporary should retry without side effects: %+v", failure)
	}
}

func TestClassifyFishErrorRequestStops(t *testing.T) {
	failure := classifyFishError(&fish.InvalidCombinationError{RequestError: fish.RequestError{Message: "Pricing configuration must match exactly one rate: matches=0", StatusCode: 400}})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.Reason != fishFailureRequest {
		t.Fatalf("reason = %q", failure.Failover.Reason)
	}
	if failure.Failover.ClientStatusCode != http.StatusBadRequest {
		t.Fatalf("client status = %d", failure.Failover.ClientStatusCode)
	}
}

func TestClassifyFishErrorIndeterminate(t *testing.T) {
	failure := classifyFishError(&fishSubmitIndeterminateError{err: errors.New("conn reset")})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d", failure.Failover.StatusCode)
	}
}

func TestClassifyFishErrorResultUnavailable(t *testing.T) {
	// 内层是 auth：账号侧处置保留（SetAccountError），但对本请求不换号
	failure := classifyFishError(&fishResultUnavailableError{
		groupID: "g",
		err:     &fish.AuthError{RequestError: fish.RequestError{StatusCode: 401}},
	})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.Reason != fishFailureResultUnavailable {
		t.Fatalf("reason = %q", failure.Failover.Reason)
	}
	if !failure.SetAccountError {
		t.Fatal("inner auth side effect must be preserved")
	}
}

func TestClassifyFishErrorGenerationFailedRetries(t *testing.T) {
	failure := classifyFishError(&fishGenerationFailedError{groupID: "g", reason: "moderation"})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountRetry {
		t.Fatalf("failover = %+v", failure.Failover)
	}
}

func TestClassifyFishErrorForeign(t *testing.T) {
	if failure := classifyFishError(errors.New("context canceled")); failure.Failover != nil {
		t.Fatalf("foreign error must yield nil failover, got %+v", failure.Failover)
	}
	if failure := classifyFishError(nil); failure.Failover != nil {
		t.Fatalf("nil must yield nil failover")
	}
}
