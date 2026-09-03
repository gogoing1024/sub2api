package service

import (
	"os"
	"strings"

	"go.uber.org/zap"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// kiroCacheUsageLogForced 由 KIRO_CACHE_USAGE_LOG=1 开启，把 kiro.cache_usage 从
// Debug 提到 Info。验证缓存来源模式时 Debug 往往没开，与其临时改代码再改回，
// 不如留一个环境变量开关。
var kiroCacheUsageLogForced = strings.TrimSpace(os.Getenv("KIRO_CACHE_USAGE_LOG")) == "1"

// kiroCacheUsageDecision 复述 mergeKiroCacheEmulationUsage 的取舍结果，供日志显式
// 标注谁胜出，省得从数字反推。三者与 merge 的分支一一对应：
//   - "no_emulation"：模拟未生效（分组未开启或 profile 被拒），merge 直接返回 base
//   - "upstream"    ：upstream_first 且上游下发过 tokenUsage，采信上游
//   - "emulation"   ：其余情形，采用本地模拟值
func kiroCacheUsageDecision(sourceMode string, hasSimulated, upstreamSeen bool) string {
	switch {
	case !hasSimulated:
		return "no_emulation"
	case sourceMode == KiroCacheSourceModeUpstreamFirst && upstreamSeen:
		return "upstream"
	default:
		return "emulation"
	}
}

// logKiroCacheUsage 打印一条三方对比日志：上游真值、本地模拟值、最终采用值，
// 外加一个显式的 decision，省得从数字反推谁胜出。
//
// 只打 token 计数与模式，不打 prompt 内容或前缀指纹——那些属于用户会话内容，
// 不应进日志。
//
// final 是 merge 之后的结果；final.Upstream 保留了上游原始快照（mergeKiroCache-
// EmulationUsage 不会覆盖它），因此单个 final 就够还原全部三方。
func logKiroCacheUsage(
	account *Account,
	group *Group,
	model string,
	sourceMode string,
	simulated *kiroCacheEmulationUsage,
	final kiropkg.Usage,
) {
	if account == nil {
		return
	}

	decision := kiroCacheUsageDecision(sourceMode, simulated != nil, final.Upstream.Seen)

	var groupID int64
	if group != nil {
		groupID = group.ID
	}

	fields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.Int64("group_id", groupID),
		zap.String("model", model),
		zap.String("source_mode", sourceMode),
		zap.String("decision", decision),
		zap.Bool("upstream_seen", final.Upstream.Seen),
		zap.Int("upstream_uncached", final.Upstream.UncachedInputTokens),
		zap.Int("upstream_output", final.Upstream.OutputTokens),
		zap.Int("upstream_total", final.Upstream.TotalTokens),
		zap.Int("upstream_cache_read", final.Upstream.CacheReadInputTokens),
		zap.Int("upstream_cache_write", final.Upstream.CacheWriteInputTokens),
		zap.Int("final_input", final.InputTokens),
		zap.Int("final_output", final.OutputTokens),
		zap.Int("final_cache_read", final.CacheReadInputTokens),
		zap.Int("final_cache_creation", final.CacheCreationInputTokens),
	}
	if simulated != nil {
		fields = append(fields,
			zap.Int("emulated_input", simulated.InputTokens),
			zap.Int("emulated_cache_read", simulated.CacheReadInputTokens),
			zap.Int("emulated_cache_creation", simulated.CacheCreationInputTokens),
		)
	}
	// RawJSON 便于发现尚未接入的字段（contextUsagePercentage / normalizedTokenUsage）。
	if final.Upstream.RawJSON != "" {
		fields = append(fields, zap.String("upstream_raw", final.Upstream.RawJSON))
	}

	if kiroCacheUsageLogForced {
		logger.L().Info("kiro.cache_usage", fields...)
		return
	}
	logger.L().Debug("kiro.cache_usage", fields...)
}
