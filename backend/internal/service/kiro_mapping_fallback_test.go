package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountKiroDefaultMappingRestrictsUnsupportedModels(t *testing.T) {
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}

	require.False(t, account.IsModelSupported("gpt-4o"))
	require.False(t, account.IsModelSupported("kiro-gpt-4o"))
	require.False(t, account.IsModelSupported("auto"))
	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-sonnet-4-6"))
}

func TestKiroDirectClaudeAliasFoldsDotPlacement(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8":          "claude-opus-4.8",
				"claude-opus-4-8-thinking": "claude-opus-4.8",
				"codex-auto-review":        "gpt-5.6-luna",
			},
		},
	}

	for _, requested := range []string{
		"claude-opus-4-8",
		"claude-opus-4.8",
		"claude-opus.4-8",
		"claude-opus-4-8-thinking",
		"claude-opus-4.8-thinking",
	} {
		require.True(t, account.IsModelSupported(requested), requested)
		require.Equal(t, "claude-opus-4.8", account.GetMappedModel(requested), requested)
	}
	require.Equal(t, "gpt-5.6-luna", account.GetMappedModel("codex-auto-review"))
	require.False(t, account.IsModelSupported("gpt-5.6.sol"))
	require.False(t, account.IsModelSupported("claude-opus-4-5-20251101"))
	require.False(t, account.IsModelSupported("claude-opus-4-5-20990101"))

	defaults := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}
	require.True(t, defaults.IsModelSupported("claude-opus-4-5-20251101"))
	require.Equal(t, "claude-opus-4.5", defaults.GetMappedModel("claude-opus-4-5-20251101"))
	require.False(t, defaults.IsModelSupported("claude-opus-4-5-20990101"))
}

func TestKiroDirectClaudeAliasPrefersExactCustomKey(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-sonnet-4.6",
				"my-opus":         "claude-opus-4.8",
			},
		},
	}

	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-opus-4.8"))
	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-opus.4-8"))
	require.Equal(t, "claude-opus-4.8", account.GetMappedModel("my-opus"))
}

func TestKiroRelayDoesNotFoldClaudeAliases(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example",
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-opus-4-8",
			},
		},
	}

	require.True(t, account.IsModelSupported("claude-opus-4-8"))
	require.Equal(t, "claude-opus-4-8", account.GetMappedModel("claude-opus-4-8"))
	require.False(t, account.IsModelSupported("claude-opus-4.8"))
	require.False(t, account.IsModelSupported("claude-opus.4-8"))
}

func TestKiroPublicCatalogKeepsUpstreamModelID(t *testing.T) {
	names, metadata := kiroPublicCatalog([]string{"claude-opus-4.8.1", "gpt-5.6-sol"})
	require.Equal(t, []string{
		"claude-opus-4-8-1",
		"claude-opus-4-8-1-thinking",
		"gpt-5.6-sol",
	}, names)
	require.Equal(t, "claude-opus-4.8.1", metadata["claude-opus-4-8-1"].ID)
	require.Equal(t, "claude-opus-4.8.1", metadata["claude-opus-4-8-1-thinking"].ID)
	require.Equal(t, "gpt-5.6-sol", metadata["gpt-5.6-sol"].ID)
}

func TestKiroIdentityMappingStillResolvesDottedUpstreamID(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-opus-4-8",
			},
		},
	}

	require.Equal(t, "claude-opus-4.8", resolveKiroUpstreamModel(account.GetMappedModel("claude-opus-4-8")))
}

func TestGatewayServiceCalculateTokenCost_KiroAutoUsesConservativeFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1

	svc := NewGatewayService(
		nil,                         // accountRepo
		nil,                         // groupRepo
		nil,                         // usageLogRepo
		nil,                         // usageBillingRepo
		nil,                         // userRepo
		nil,                         // userSubRepo
		nil,                         // userGroupRateRepo
		nil,                         // cache
		cfg,                         // cfg
		nil,                         // schedulerSnapshot
		nil,                         // concurrencyService
		NewBillingService(cfg, nil), // billingService
		nil,                         // rateLimitService
		nil,                         // billingCacheService
		nil,                         // identityService
		nil,                         // httpUpstream
		nil,                         // deferredService
		nil,                         // claudeTokenProvider
		nil,                         // kiroTokenProvider
		nil,                         // adobeTokenProvider
		nil,                         // kiroCooldownStore
		nil,                         // sessionLimitCache
		nil,                         // rpmCache
		nil,                         // digestStore
		nil,                         // settingService
		nil,                         // tlsFPProfileService
		nil,                         // channelService
		nil,                         // resolver
		nil,                         // compositeResolver
		nil,                         // balanceNotifyService
		nil,                         // userPlatformQuotaRepo
	)

	result := &ForwardResult{
		Model:         "auto",
		UpstreamModel: "auto",
		Usage: ClaudeUsage{
			InputTokens:  20,
			OutputTokens: 10,
		},
	}

	expected, err := svc.billingService.CalculateCost(kiroConservativeFallbackBillingModel, UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{}, "auto", 1.1, time.Time{}, &recordUsageOpts{IsKiroAccount: true})
	require.NotNil(t, cost)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-12)
	require.InDelta(t, expected.TotalCost, cost.TotalCost, 1e-12)
}
