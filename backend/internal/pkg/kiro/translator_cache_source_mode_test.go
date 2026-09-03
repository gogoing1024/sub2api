package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateUsageFromEventParsesUpstreamTokenUsage(t *testing.T) {
	var usage Usage

	updateUsageFromEvent(&usage, "metadataEvent", map[string]any{
		"metadataEvent": map[string]any{
			"tokenUsage": map[string]any{
				"uncachedInputTokens":   11,
				"outputTokens":          22,
				"totalTokens":           99,
				"cacheReadInputTokens":  33,
				"cacheWriteInputTokens": 44,
			},
		},
	})

	// cacheWriteInputTokens 此前漏解析，会让上游真值永远填不进 cache_creation。
	require.Equal(t, 44, usage.CacheCreationInputTokens)
	require.Equal(t, 33, usage.CacheReadInputTokens)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 22, usage.OutputTokens)
	require.Equal(t, 99, usage.TotalTokens)

	require.True(t, usage.Upstream.Seen)
	require.Equal(t, 11, usage.Upstream.UncachedInputTokens)
	require.Equal(t, 22, usage.Upstream.OutputTokens)
	require.Equal(t, 99, usage.Upstream.TotalTokens)
	require.Equal(t, 33, usage.Upstream.CacheReadInputTokens)
	require.Equal(t, 44, usage.Upstream.CacheWriteInputTokens)
	require.Contains(t, usage.Upstream.RawJSON, "cacheWriteInputTokens")
}

func TestUpdateUsageFromEventMarksSeenEvenWhenAllZero(t *testing.T) {
	var usage Usage

	// 上游明确报「本次零缓存」也是权威结论：Seen 必须置位，
	// 否则 upstream_first 会退回模拟、凭空造出命中值。
	updateUsageFromEvent(&usage, "metadataEvent", map[string]any{
		"metadataEvent": map[string]any{
			"tokenUsage": map[string]any{
				"uncachedInputTokens":   0,
				"outputTokens":          0,
				"cacheReadInputTokens":  0,
				"cacheWriteInputTokens": 0,
			},
		},
	})

	require.True(t, usage.Upstream.Seen)
	require.Equal(t, 0, usage.CacheReadInputTokens)
	require.Equal(t, 0, usage.CacheCreationInputTokens)
}

func TestUpdateUsageFromEventLeavesUpstreamUnseenWithoutTokenUsage(t *testing.T) {
	var usage Usage

	updateUsageFromEvent(&usage, "meteringEvent", map[string]any{
		"meteringEvent": map[string]any{"usage": 0.12},
	})

	require.False(t, usage.Upstream.Seen)
	require.Empty(t, usage.Upstream.RawJSON)
}

func TestMergeKiroCacheEmulationUsageUpstreamFirstPrefersUpstream(t *testing.T) {
	base := Usage{
		InputTokens:              11,
		OutputTokens:             22,
		CacheReadInputTokens:     33,
		CacheCreationInputTokens: 44,
		Upstream: UpstreamTokenUsage{
			Seen:                  true,
			UncachedInputTokens:   11,
			CacheReadInputTokens:  33,
			CacheWriteInputTokens: 44,
		},
	}
	simulated := &Usage{
		InputTokens:              500,
		CacheReadInputTokens:     600,
		CacheCreationInputTokens: 700,
	}

	got := mergeKiroCacheEmulationUsage(base, simulated, KiroCacheSourceModeUpstreamFirst)

	require.Equal(t, 11, got.InputTokens)
	require.Equal(t, 33, got.CacheReadInputTokens)
	require.Equal(t, 44, got.CacheCreationInputTokens)
}

func TestMergeKiroCacheEmulationUsageUpstreamFirstKeepsZeroUpstream(t *testing.T) {
	// 关键用例：上游报了 tokenUsage 但缓存全零 → 采信零值，不退回模拟。
	base := Usage{
		InputTokens:  120,
		OutputTokens: 7,
		Upstream: UpstreamTokenUsage{
			Seen:                true,
			UncachedInputTokens: 120,
		},
	}
	simulated := &Usage{
		InputTokens:              20,
		CacheReadInputTokens:     60,
		CacheCreationInputTokens: 40,
	}

	got := mergeKiroCacheEmulationUsage(base, simulated, KiroCacheSourceModeUpstreamFirst)

	require.Equal(t, 120, got.InputTokens)
	require.Equal(t, 0, got.CacheReadInputTokens)
	require.Equal(t, 0, got.CacheCreationInputTokens)
}

func TestMergeKiroCacheEmulationUsageUpstreamFirstFallsBackWhenUnseen(t *testing.T) {
	base := Usage{OutputTokens: 9}
	simulated := &Usage{
		InputTokens:                20,
		CacheReadInputTokens:       60,
		CacheCreationInputTokens:   40,
		CacheCreation5mInputTokens: 40,
	}

	got := mergeKiroCacheEmulationUsage(base, simulated, KiroCacheSourceModeUpstreamFirst)

	require.Equal(t, 20, got.InputTokens)
	require.Equal(t, 60, got.CacheReadInputTokens)
	require.Equal(t, 40, got.CacheCreationInputTokens)
	require.Equal(t, 40, got.CacheCreation5mInputTokens)
	require.Equal(t, 20+9+60+40, got.TotalTokens)
}

func TestMergeKiroCacheEmulationUsageEmulationOnlyOverridesUpstream(t *testing.T) {
	base := Usage{
		InputTokens:              11,
		OutputTokens:             5,
		CacheReadInputTokens:     33,
		CacheCreationInputTokens: 44,
		Upstream: UpstreamTokenUsage{
			Seen:                  true,
			UncachedInputTokens:   11,
			CacheReadInputTokens:  33,
			CacheWriteInputTokens: 44,
			RawJSON:               `{"cacheReadInputTokens":33}`,
		},
	}
	simulated := &Usage{
		InputTokens:              20,
		CacheReadInputTokens:     60,
		CacheCreationInputTokens: 40,
	}

	got := mergeKiroCacheEmulationUsage(base, simulated, KiroCacheSourceModeEmulationOnly)

	require.Equal(t, 20, got.InputTokens)
	require.Equal(t, 60, got.CacheReadInputTokens)
	require.Equal(t, 40, got.CacheCreationInputTokens)
	// 空串（未配置）与 emulation_only 同义。
	require.Equal(t, got, mergeKiroCacheEmulationUsage(base, simulated, ""))

	// Upstream 原值必须原样保留，诊断日志依赖它做三方对比。
	require.True(t, got.Upstream.Seen)
	require.Equal(t, 33, got.Upstream.CacheReadInputTokens)
	require.Equal(t, 44, got.Upstream.CacheWriteInputTokens)
	require.Equal(t, `{"cacheReadInputTokens":33}`, got.Upstream.RawJSON)
}

func TestMergeKiroCacheEmulationUsageNilSimulatedReturnsBase(t *testing.T) {
	base := Usage{InputTokens: 7, Upstream: UpstreamTokenUsage{Seen: true}}

	require.Equal(t, base, mergeKiroCacheEmulationUsage(base, nil, KiroCacheSourceModeEmulationOnly))
	require.Equal(t, base, mergeKiroCacheEmulationUsage(base, nil, KiroCacheSourceModeUpstreamFirst))
}
