package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// fishRateLimitCooldown 是上游 429 后账号的冷却时长。
const fishRateLimitCooldown = 5 * time.Minute

// fishInsufficientCooldown 是 credits 不足/配额耗尽（402 OUT_OF_CREDITS）
// 时的冷却时长。上游错误不带 resetAt；free 档 ~6900 credits/天的共享预算
// 在 UTC 零点重置，但钱包/日赠额语义未定——2h 意味着每天至多 ~12 次免费
// 探测（402 预检拒绝不扣费），比到零点保守、比短冷却省探测。抓到带
// resetAt 的样本后升级为解析式。
const fishInsufficientCooldown = 2 * time.Hour

// fish 失败原因，进入 UpstreamFailoverError.Reason 供 ops 归因。
const (
	fishFailureInsufficientCredits = GatewayFailureReason("fish_insufficient_credits")
	fishFailureAuth                = GatewayFailureReason("fish_auth")
	fishFailureUpstream            = GatewayFailureReason("fish_upstream")
	fishFailureRequest             = GatewayFailureReason("fish_request")
	fishFailureGenerationFailed    = GatewayFailureReason("fish_generation_failed")
	fishFailureIndeterminate       = GatewayFailureReason("fish_indeterminate")
	fishFailureResultUnavailable   = GatewayFailureReason("fish_result_unavailable")
)

// fishFailure 是 pkg/fish 的错误翻译成网关语义后的结果（镜像 leonardoFailure）。
type fishFailure struct {
	Failover           *UpstreamFailoverError
	Cooldown           time.Duration
	CooldownReason     string
	SetAccountError    bool
	AccountErrorMessage string
}

// classifyFishError 把 fish 链路的错误翻译成网关语义。
func classifyFishError(err error) fishFailure {
	if err == nil {
		return fishFailure{}
	}

	// 提交结果不明优先于一切：上游可能已受理并扣积分，绝不能换号重提。
	var indeterminate *fishSubmitIndeterminateError
	if errors.As(err, &indeterminate) {
		return fishFailure{Failover: fishIndeterminateFailure()}
	}

	// 已提交但取不回结果：同样绝不换号重提。账号侧处置按内层错误执行。
	var unavailable *fishResultUnavailableError
	if errors.As(err, &unavailable) {
		failure := classifyFishError(unavailable.err)
		failure.Failover = fishResultUnavailableFailure(unavailable.err)
		return failure
	}

	// 上游显式失败（积分已退）：可换号重试。
	var generationFailed *fishGenerationFailedError
	if errors.As(err, &generationFailed) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        http.StatusBadGateway,
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureGenerationFailed,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	var insufficientErr *fish.InsufficientCreditsError
	if errors.As(err, &insufficientErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(insufficientErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeAccount,
				Reason:            fishFailureInsufficientCredits,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       fishInsufficientCooldown,
			CooldownReason: "fish credits exhausted or quota exceeded",
		}
	}

	var authErr *fish.AuthError
	if errors.As(err, &authErr) {
		// fish token 即长期凭据（无 refresh）：401 = 账号失效，直接置 error
		// 让管理员换号，比冷却更诚实。
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(authErr.StatusCode, http.StatusUnauthorized),
				Stage:             GatewayFailureStageAccountAuth,
				Scope:             GatewayFailureScopeAccount,
				Reason:            fishFailureAuth,
				NextAccountAction: NextAccountRetry,
			},
			SetAccountError:     true,
			AccountErrorMessage: "fish token rejected by upstream (invalid token)",
		}
	}

	var rateLimitedErr *fish.RateLimitedError
	if errors.As(err, &rateLimitedErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(rateLimitedErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       fishRateLimitCooldown,
			CooldownReason: "fish rate limited",
		}
	}

	var temporaryErr *fish.UpstreamTemporaryError
	if errors.As(err, &temporaryErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(temporaryErr.StatusCode, http.StatusBadGateway),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	// 终态：请求参数问题（组合非法、模型不支持、尺寸越界等），换号无解。
	var requestErr *fish.RequestError
	if errors.As(err, &requestErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(requestErr.StatusCode, http.StatusBadRequest),
				ClientStatusCode:  http.StatusBadRequest,
				ClientMessage:     requestErr.Error(),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeRequest,
				Reason:            fishFailureRequest,
				NextAccountAction: NextAccountStop,
			},
		}
	}

	// 非本包错误（ctx 取消、参数校验等）：不换号，调用方原样上抛。
	return fishFailure{}
}

// fishIndeterminateFailure 构造「提交结果不明」的 failover 决策：504、不换号。
func fishIndeterminateFailure() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusGatewayTimeout,
		ClientStatusCode:  http.StatusGatewayTimeout,
		ClientMessage:     "fish generation submit result is unknown; not retrying to avoid double billing",
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            fishFailureIndeterminate,
		NextAccountAction: NextAccountStop,
	}
}

// fishResultUnavailableFailure 构造「已提交但取不回结果」的 failover 决策：
// 超出脱钩时限报 504，其余报 502；均不换号。
func fishResultUnavailableFailure(cause error) *UpstreamFailoverError {
	status := http.StatusBadGateway
	message := "fish generation was submitted but its result could not be retrieved; not retrying to avoid double billing"
	if errors.Is(cause, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		message = "fish generation did not finish in time; not retrying to avoid double billing"
	}
	return &UpstreamFailoverError{
		StatusCode:        status,
		ClientStatusCode:  status,
		ClientMessage:     message,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            fishFailureResultUnavailable,
		NextAccountAction: NextAccountStop,
	}
}

// FishFailover 把 fish 链路的错误翻译成网关的 failover 决策，并按需冷却
// 账号、置 error。handler 层的唯一入口；返回 nil 表示错误不属于 fish 上游
// 语义，调用方应原样上抛。
func (s *GatewayService) FishFailover(ctx context.Context, accountID int64, _ string, err error) *UpstreamFailoverError {
	failure := classifyFishError(err)
	if failure.Failover == nil {
		return nil
	}
	if s != nil {
		applyFishCooldown(ctx, s.accountRepo, accountID, failure)
		applyFishAccountError(ctx, s.accountRepo, accountID, failure)
	}
	return failure.Failover
}

func applyFishCooldown(ctx context.Context, repo AccountRepository, accountID int64, failure fishFailure) {
	if repo == nil || accountID <= 0 || failure.Cooldown <= 0 {
		return
	}
	until := time.Now().Add(failure.Cooldown)
	if err := repo.SetTempUnschedulable(ctx, accountID, until, failure.CooldownReason); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.cooldown_failed",
			zap.Int64("account_id", accountID),
			zap.String("reason", failure.CooldownReason),
			zap.Error(err),
		)
	}
}

func applyFishAccountError(ctx context.Context, repo AccountRepository, accountID int64, failure fishFailure) {
	if !failure.SetAccountError || repo == nil || accountID <= 0 {
		return
	}
	if err := repo.SetError(ctx, accountID, failure.AccountErrorMessage); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.account_error_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}
