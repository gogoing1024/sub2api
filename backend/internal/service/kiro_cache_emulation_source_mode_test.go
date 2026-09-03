package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// upstream_first 下模拟器仍须照常维护前缀链并 commit：这样上游偶发不下发
// tokenUsage 时，兜底能立刻给出有意义的 cache_read，而不是冷启动全记 creation。
//
// 实现上这由「模式只影响 merge、不影响 emulation」保证；本测试把该不变量钉住，
// 防止后来有人以「upstream_first 不需要模拟」为由跳过 prepare/commit。
func TestKiroCacheEmulationStillTracksPrefixUnderUpstreamFirst(t *testing.T) {
	resetKiroCacheTracker()
	svc := &GatewayService{}
	account := &Account{ID: 77, Platform: PlatformKiro}

	group := kiroCacheGroup(1)
	group.KiroCacheSourceMode = KiroCacheSourceModeUpstreamFirst
	NormalizeGroupRuntimeFields(group)
	require.Equal(t, KiroCacheSourceModeUpstreamFirst, group.EffectiveKiroCacheSourceMode())

	body := kiroCacheRequestBody("upstream first prefix", false)

	first := svc.buildKiroCacheEmulationUsage(context.Background(), account, group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, first)
	require.Equal(t, 2000, first.CacheCreationInputTokens)
	require.Equal(t, 0, first.CacheReadInputTokens)

	// 第二轮命中，证明第一轮确实 commit 进了 tracker。
	second := svc.buildKiroCacheEmulationUsage(context.Background(), account, group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, second)
	require.Equal(t, 2000, second.CacheReadInputTokens)
	require.Equal(t, 0, second.CacheCreationInputTokens)
}

// 缓存来源模式与「是否启用模拟」正交：开关关闭时不模拟，与来源模式无关。
func TestKiroCacheSourceModeIsOrthogonalToEmulationToggle(t *testing.T) {
	resetKiroCacheTracker()
	svc := &GatewayService{}
	account := &Account{ID: 78, Platform: PlatformKiro}

	group := kiroCacheGroup(1)
	group.KiroCacheEmulationEnabled = false
	group.KiroCacheSourceMode = KiroCacheSourceModeUpstreamFirst
	NormalizeGroupRuntimeFields(group)

	usage := svc.buildKiroCacheEmulationUsage(
		context.Background(), account, group,
		kiroCacheRequestBody("disabled", false), "claude-sonnet-4-6", 2000,
	)
	require.Nil(t, usage, "emulation disabled must yield no simulated usage regardless of source mode")
}

// 比例模式（uniform/independent）与来源模式互不干扰：两者可同时配置。
func TestKiroCacheSourceModeCoexistsWithRatioMode(t *testing.T) {
	resetKiroCacheTracker()
	svc := &GatewayService{}
	account := &Account{ID: 79, Platform: PlatformKiro}

	group := kiroCacheGroup(0.5)
	group.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
	group.KiroCacheSourceMode = KiroCacheSourceModeUpstreamFirst
	NormalizeGroupRuntimeFields(group)

	require.Equal(t, KiroCacheEmulationModeUniform, group.EffectiveKiroCacheEmulationMode())
	require.Equal(t, KiroCacheSourceModeUpstreamFirst, group.EffectiveKiroCacheSourceMode())

	usage := svc.buildKiroCacheEmulationUsage(
		context.Background(), account, group,
		kiroCacheRequestBody("ratio plus source", false), "claude-sonnet-4-6", 2000,
	)
	require.NotNil(t, usage)
	// 比例 0.5 仍照常作用于模拟值。
	require.Equal(t, 1000, usage.CacheCreationInputTokens)
}
