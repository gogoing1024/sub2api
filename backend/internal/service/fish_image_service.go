package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// fish 账号的凭据键。token 必填；team_id / device_id 由账号连通性测试
// 自动派生回填（派生前请求照发——上游已验证这些头可省）。
const (
	fishCredentialToken    = "token"
	fishCredentialTeamID   = "team_id"
	fishCredentialDeviceID = "device_id"
	// fishCredentialMaxPerGen 是账号单次生图上限（entitlements
	// maxImagesPerGeneration：free=2 / plus=4 / pro=8），连通性测试回填。
	fishCredentialMaxPerGen = "max_images_per_generation"
)

const (
	fishSubmitBudget        = 60 * time.Second
	fishPollTimeout         = 10 * time.Minute
	fishMaxImagesPerRequest = 8 // 模型 maxCount；free 档配额由上游拒绝
	fishDownloadAttempts    = 3
)

// fishClientCache 按账号缓存带代理的客户端实例（连接复用；客户端无状态，
// 按代理指纹缓存即可）。代理变化时重建。镜像 leonardoClientCache。
type fishClientCache struct {
	mu      sync.Mutex
	clients map[int64]*fishClientEntry
	options fish.Options
}

type fishClientEntry struct {
	client   *fish.Client
	proxyURL string
}

func (c *fishClientCache) clientForAccount(account *Account) *fish.Client {
	proxyURL := accountProxyURL(account)
	if account == nil || account.ID <= 0 {
		return c.newClient(proxyURL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = make(map[int64]*fishClientEntry)
	}
	if entry, ok := c.clients[account.ID]; ok && entry.proxyURL == proxyURL {
		return entry.client
	}
	client := c.newClient(proxyURL)
	c.clients[account.ID] = &fishClientEntry{client: client, proxyURL: proxyURL}
	return client
}

func (c *fishClientCache) newClient(proxyURL string) *fish.Client {
	opts := c.options
	opts.ProxyURL = proxyURL
	return fish.NewClient(opts)
}

// 轮询间隔用变量便于测试注入。
var (
	fishPollInitialInterval = 2 * time.Second
	fishPollMaxInterval     = 15 * time.Second
)

// FishImageService 实现 fish.audio 生图（OpenAI Images 兼容入口）。
type FishImageService struct {
	clients        *fishClientCache
	resolveStorage ImageStorageResolver
	// inputClient 抓取参考图源图（复用 leonardo_image_input.go 的
	// SSRF 三层防护客户端）。
	inputClient *http.Client
}

// NewFishImageService 构造服务。
func NewFishImageService(resolveStorage ImageStorageResolver) *FishImageService {
	return &FishImageService{
		clients:        &fishClientCache{},
		resolveStorage: resolveStorage,
		inputClient:    newLeonardoInputImageClient(),
	}
}

// FishImageResult 是一次 fish 生图的结果。
type FishImageResult struct {
	// Body 是 OpenAI Images 形状的完整 JSON 响应（data[].b64_json 或经
	// 对象存储转存后的 data[].url）。
	Body []byte
	// Forward 供计费记账（ImageCount / ImageSize 档位）。
	Forward *OpenAIForwardResult
	// GroupID 是上游任务组 id（排障用）。
	GroupID string
	// Size / Quality 是回显给客户端的请求参数。
	Size    string
	Quality string
}

// fishInputImage 是一张待上传的参考图（复用 leonardoInputImage 形状）。
type fishInputImage = leonardoInputImage

// FishImageCall 跨账号切换保持的请求级状态：参考图只取一次；已交付的图
// 与剩余张数在换号续跑间累计——重跑只生成缺口，绝不重生成已交付图（防双扣）。
type FishImageCall struct {
	Request            *OpenAIImagesRequest
	ChannelMappedModel string
	inputsOnce         sync.Once
	inputs             []*fishInputImage
	inputsErr          error
	// delivered 是已下载入内存的成品图（跨账号累计）。
	delivered [][]byte
	// remaining 是还需生成的张数（NewFishImageCall 时 = n）。
	remaining int
}

// NewFishImageCall 构造一次调用的共享状态。
func NewFishImageCall(req *OpenAIImagesRequest, channelMappedModel string) *FishImageCall {
	n := 1
	if req != nil && req.N > 1 {
		n = req.N
	}
	return &FishImageCall{Request: req, ChannelMappedModel: channelMappedModel, remaining: n}
}

// Remaining 返回还需生成的张数（handler 日志用）。
func (c *FishImageCall) Remaining() int {
	if c == nil {
		return 0
	}
	return c.remaining
}

// Delivered 返回已交付张数（handler 判断部分成功用）。
func (c *FishImageCall) Delivered() int {
	if c == nil {
		return 0
	}
	return len(c.delivered)
}

// Generate 是 GenerateCall 的薄封装（单账号直调 / 测试用）。
func (s *FishImageService) Generate(ctx context.Context, account *Account, token string, req *OpenAIImagesRequest) (*FishImageResult, error) {
	return s.GenerateCall(ctx, account, token, NewFishImageCall(req, req.Model))
}

// fishIdentityFromAccount 从账号凭据构造指纹身份。
func fishIdentityFromAccount(account *Account) fish.Identity {
	return fish.Identity{
		DeviceID: account.GetCredential(fishCredentialDeviceID),
		TeamID:   account.GetCredential(fishCredentialTeamID),
	}
}

// accountFishImageCap 返回账号单次生图上限：凭据回填的
// max_images_per_generation（连通性测试写入），非法/缺失回落模型层 8。
func accountFishImageCap(account *Account) int {
	if account == nil {
		return fishMaxImagesPerRequest
	}
	if cap, err := strconv.Atoi(strings.TrimSpace(account.GetCredential(fishCredentialMaxPerGen))); err == nil &&
		cap >= 1 && cap <= fishMaxImagesPerRequest {
		return cap
	}
	return fishMaxImagesPerRequest
}

// fishPendingBatch 是提交段产出：一个已受理的任务组及其张数。
type fishPendingBatch struct {
	groupID string
	count   int
}

// GenerateCall 在指定账号上尽量完成 call.remaining 张图（三段式）：
//
//  1. 提交段（串行）：n > 账号单次上限时拆多个子生成（计价按张线性，
//     拆分不改变总成本）。提交错分类处理：网络错=结果不明（停止提交，
//     已提交的照走完）；配额/429/5xx 等可换号错（停止提交，走完已提交
//     的，错误随进度返回——handler 换号续跑只生成缺口）。
//  2. 轮询段：逐组 poll 到终态；failed 的组不交付（remaining 不减）。
//  3. 下载段：done 的产物下载进 call.delivered。
//
// remaining 清零 → 用全部 delivered（含前序账号进度）构建响应；
// remaining > 0 → 返回「最有分量的错误」（提交错优先于组内 failed）。
func (s *FishImageService) GenerateCall(ctx context.Context, account *Account, token string, call *FishImageCall) (*FishImageResult, error) {
	if call == nil || call.Request == nil {
		return nil, fmt.Errorf("fish: nil call")
	}
	parsed := call.Request
	mappedModel := strings.TrimSpace(call.ChannelMappedModel)
	if mappedModel == "" {
		mappedModel = strings.TrimSpace(parsed.Model)
	}
	model, ok := domain.LookupFishModel(mappedModel)
	if !ok {
		return nil, fish.NewRequestError(fmt.Sprintf("fish: model %q is not a fish image model", mappedModel))
	}
	imageSize, err := domain.FishResolveSize(model, parsed.Size)
	if err != nil {
		return nil, fish.NewRequestError(err.Error())
	}
	quality := fishQualityForRequest(parsed.Quality)
	if call.remaining < 1 {
		call.remaining = 1
	}
	if call.remaining > fishMaxImagesPerRequest {
		return nil, fish.NewRequestError(fmt.Sprintf("fish: n=%d exceeds max %d per generation", call.remaining, fishMaxImagesPerRequest))
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return nil, fish.NewRequestError("fish: prompt is required")
	}

	client := s.clients.clientForAccount(account)
	identity := fishIdentityFromAccount(account)
	cap := accountFishImageCap(account)

	// 参考图（/v1/images/edits）：跨账号只取一次
	var refAssets []fish.RefAsset
	if wantsFishImageEdits(parsed) {
		inputs, err := s.loadInputs(ctx, call)
		if err != nil {
			return nil, err
		}
		for _, input := range inputs {
			asset, err := client.UploadRefImage(ctx, token, identity, "image.png", input.ContentType, input.Data)
			if err != nil {
				return nil, err
			}
			refAssets = append(refAssets, *asset)
		}
	}
	variant := fish.VariantTextToImage
	if len(refAssets) > 0 {
		variant = fish.VariantEdit
	}

	// ── 提交段（串行）──
	var pending []fishPendingBatch
	var submitErr error      // 可换号的提交错误（配额/429/5xx…）
	var indeterminate error  // 提交结果不明（网络错）
	remaining := call.remaining
	for remaining > 0 {
		batch := minInt(cap, remaining)
		price, err := client.Price(ctx, token, identity, fish.PriceParams{
			Model: model.Name, Variant: variant, ImageSize: imageSize,
			Quality: quality, Count: batch, RefImageCount: len(refAssets),
		})
		if err != nil {
			submitErr = err
			break
		}
		submitCtx, cancel := context.WithTimeout(ctx, fishSubmitBudget)
		group, err := client.SubmitGeneration(submitCtx, token, identity, fish.GenerateRequest{
			Model: model.Name, Variant: variant, Prompt: parsed.Prompt,
			Count: batch, ImageSize: imageSize, Quality: quality,
			RefAssets: refAssets, ExpectedCredits: price.Credits, PricingVersion: price.PricingVersion,
		})
		cancel()
		if err != nil {
			if isFishNetworkErr(err) {
				indeterminate = &fishSubmitIndeterminateError{err: err}
			} else {
				submitErr = err
			}
			break
		}
		pending = append(pending, fishPendingBatch{groupID: group.ID, count: batch})
		remaining -= batch
	}
	// 提交段一个都没成且无不明 → 参数类错误直接终态（没有进度可言）
	if len(pending) == 0 && indeterminate == nil && submitErr != nil && !isFishFailoverClass(submitErr) {
		return nil, submitErr
	}

	// ── 轮询段 + 下载段（脱离请求生命周期；客户端断开不打断）──
	pollCtx, pollCancel := context.WithTimeout(context.WithoutCancel(ctx), fishPollTimeout)
	defer pollCancel()
	var lastGroupErr error
	for _, batch := range pending {
		finalGroup, err := s.pollUntilDone(pollCtx, client, token, identity, batch.groupID)
		if err != nil {
			var generationFailed *fishGenerationFailedError
			if errors.As(err, &generationFailed) {
				// 组内失败（审核等）：这批不交付，继续下一组
				lastGroupErr = err
				continue
			}
			// 轮询/结果不可得：保留进度，错误上抛（不换号重提语义）
			call.remaining = clampRemaining(call, pending, batch)
			return nil, err
		}
		mediaItems := collectFishMedia(finalGroup)
		if len(mediaItems) == 0 {
			lastGroupErr = &fishResultUnavailableError{groupID: batch.groupID, err: fmt.Errorf("task finished without media")}
			continue
		}
		for _, item := range mediaItems {
			data, err := s.downloadWithRetry(pollCtx, client, item.URL)
			if err != nil {
				call.remaining = clampRemaining(call, pending, batch)
				return nil, &fishResultUnavailableError{groupID: batch.groupID, err: err}
			}
			call.delivered = append(call.delivered, data)
		}
	}
	// remaining 重算：n - 已交付
	call.remaining = callInitialN(call) - len(call.delivered)
	if call.remaining <= 0 {
		body, err := s.buildResponseBody(ctx, call.delivered, imageSize, quality)
		if err != nil {
			return nil, err
		}
		return &FishImageResult{
			Body: body,
			Forward: &OpenAIForwardResult{
				Model:      parsed.Model,
				ImageCount: len(call.delivered),
				ImageSize:  fishBillingSize(imageSize),
			},
			GroupID: fishFirstGroupID(pending),
			Size:    imageSize,
			Quality: quality,
		}, nil
	}

	// 还有缺口：按分量返回错误（不明 > 可换号提交错 > 组内失败）
	if indeterminate != nil {
		return nil, indeterminate
	}
	if submitErr != nil {
		return nil, submitErr
	}
	if lastGroupErr != nil {
		return nil, lastGroupErr
	}
	return nil, &fishResultUnavailableError{groupID: fishFirstGroupID(pending), err: fmt.Errorf("generation incomplete: %d of %d delivered", len(call.delivered), callInitialN(call))}
}

// callInitialN 返回本次请求的初始张数。
func callInitialN(call *FishImageCall) int {
	n := 1
	if call != nil && call.Request != nil && call.Request.N > 1 {
		n = call.Request.N
	}
	return n
}

// clampRemaining 在轮询/下载中途失败时重算缺口 = 初始 n - 已交付。
func clampRemaining(call *FishImageCall, pending []fishPendingBatch, failed fishPendingBatch) int {
	return callInitialN(call) - len(call.delivered)
}

func fishFirstGroupID(pending []fishPendingBatch) string {
	if len(pending) > 0 {
		return pending[0].groupID
	}
	return ""
}

// isFishFailoverClass 报告错误是否属于可换号类（配额/限流/暂态/显式失败）。
func isFishFailoverClass(err error) bool {
	failure := classifyFishError(err)
	return failure.Failover != nil && failure.Failover.ShouldRetryNextAccount()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BuildFishPartialResult 为「有进度但终止」的请求构建部分成功响应：
// 张数=实际交付数，计费按实际张数。调用方（handler）负责日志标注 partial。
func (s *FishImageService) BuildFishPartialResult(ctx context.Context, call *FishImageCall) *FishImageResult {
	if call == nil || call.Request == nil || len(call.delivered) == 0 {
		return nil
	}
	parsed := call.Request
	shape := domainFishResolveLoose(parsed)
	body, err := s.buildResponseBody(ctx, call.delivered, shape.size, shape.quality)
	if err != nil {
		return nil
	}
	return &FishImageResult{
		Body: body,
		Forward: &OpenAIForwardResult{
			Model:      parsed.Model,
			ImageCount: len(call.delivered),
			ImageSize:  fishBillingSize(shape.size),
		},
		Size:    shape.size,
		Quality: shape.quality,
	}
}

// fishResolvedShape 是已解析的尺寸/质量（BuildFishPartialResult 用）。
type fishResolvedShape struct{ size, quality string }

func domainFishResolveLoose(parsed *OpenAIImagesRequest) fishResolvedShape {
	mapped := strings.TrimSpace(parsed.Model)
	model, ok := domain.LookupFishModel(mapped)
	if !ok {
		if len(domain.FishModels) > 0 {
			model = domain.FishModels[0]
		}
	}
	size, err := domain.FishResolveSize(model, parsed.Size)
	if err != nil {
		size = "1024x1024"
	}
	return fishResolvedShape{size: size, quality: fishQualityForRequest(parsed.Quality)}
}

// pollUntilDone 轮询任务组到终态。429/5xx/网络是暂态（退避重试）；
// 上游显式 failed 才是可重试的失败（积分已退）。
func (s *FishImageService) pollUntilDone(ctx context.Context, client *fish.Client, token string, identity fish.Identity, groupID string) (*fish.TaskGroup, error) {
	interval := fishPollInitialInterval
	for {
		group, err := client.PollTaskGroup(ctx, token, identity, groupID)
		if err != nil {
			if isFishTransientErr(err) {
				select {
				case <-time.After(interval):
					interval = minDuration(interval*2, fishPollMaxInterval)
					continue
				case <-ctx.Done():
					return nil, &fishResultUnavailableError{groupID: groupID, err: ctx.Err()}
				}
			}
			return nil, err
		}
		switch group.Status {
		case "done":
			return group, nil
		case "failed":
			return nil, &fishGenerationFailedError{
				groupID: groupID,
				reason:  fishFailureSummary(group),
			}
		}
		select {
		case <-time.After(interval):
			interval = minDuration(interval*2, fishPollMaxInterval)
		case <-ctx.Done():
			return nil, &fishResultUnavailableError{groupID: groupID, err: ctx.Err()}
		}
	}
}

func (s *FishImageService) downloadWithRetry(ctx context.Context, client *fish.Client, imageURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < fishDownloadAttempts; attempt++ {
		data, err := client.DownloadImage(ctx, imageURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// buildResponseBody 构造 OpenAI Images 形状响应；对象存储启用时把
// b64 换存 URL（失败回退 b64，与 Leonardo 同策略）。
func (s *FishImageService) buildResponseBody(ctx context.Context, images [][]byte, size, quality string) ([]byte, error) {
	data := make([]map[string]any, 0, len(images))
	for _, image := range images {
		data = append(data, map[string]any{
			"b64_json": base64.StdEncoding.EncodeToString(image),
		})
	}
	bodyMap := map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	}
	if strings.TrimSpace(size) != "" {
		bodyMap["size"] = size
	}
	if strings.TrimSpace(quality) != "" {
		bodyMap["quality"] = quality
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("fish: encode response body: %w", err)
	}
	if s.resolveStorage != nil {
		if uploader, enabled := s.resolveStorage(); enabled && uploader != nil {
			if rewritten, rerr := uploader.Rewrite(ctx, fmt.Sprintf("fish-%d", time.Now().UnixNano()), body); rerr == nil {
				return rewritten, nil
			}
			// 转存失败回退 b64 响应
		}
	}
	return body, nil
}

// loadInputs 取 edits 的参考图（multipart 直传数据 + URL 抓取），跨账号
// 只执行一次。
func (s *FishImageService) loadInputs(ctx context.Context, call *FishImageCall) ([]*fishInputImage, error) {
	call.inputsOnce.Do(func() {
		inputs := make([]*fishInputImage, 0, len(call.Request.Uploads)+len(call.Request.InputImageURLs))
		for i := range call.Request.Uploads {
			upload := call.Request.Uploads[i]
			if len(upload.Data) == 0 {
				call.inputsErr = fmt.Errorf("fish: uploaded input image is empty")
				return
			}
			inputs = append(inputs, &fishInputImage{Data: upload.Data, ContentType: upload.ContentType})
		}
		for _, rawURL := range call.Request.InputImageURLs {
			input, err := fetchLeonardoInputImage(ctx, s.inputClient, rawURL)
			if err != nil {
				call.inputsErr = err
				return
			}
			inputs = append(inputs, input)
		}
		if len(inputs) == 0 {
			call.inputsErr = fmt.Errorf("fish: image edits require an input image")
			return
		}
		call.inputs = inputs
	})
	return call.inputs, call.inputsErr
}

// ── 内部工具 ──

// fishQualityForRequest 归一 OpenAI 质量（standard/hd）到 fish 档位。
// gpt-image 家族恒支持 quality 三档。
func fishQualityForRequest(requested string) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "low":
		return "low"
	case "high", "hd":
		return "high"
	default: // "", "auto", "medium", "standard"
		return "medium"
	}
}

// fishBillingSize 把 fish 的 image_size 值映射为计费档位。
func fishBillingSize(imageSize string) string {
	if tier, ok := ClassifyImageBillingTier(imageSize); ok {
		return tier
	}
	return NormalizeImageBillingTierOrDefault("")
}

func wantsFishImageEdits(parsed *OpenAIImagesRequest) bool {
	if parsed == nil {
		return false
	}
	return len(parsed.InputImageURLs) > 0 || len(parsed.Uploads) > 0
}

func collectFishMedia(group *fish.TaskGroup) []*fish.MediaItem {
	if group == nil {
		return nil
	}
	items := make([]*fish.MediaItem, 0, len(group.Results))
	for i := range group.Results {
		if item := group.Results[i].MediaItem; item != nil && strings.TrimSpace(item.URL) != "" {
			items = append(items, item)
		}
	}
	return items
}

func fishFailureSummary(group *fish.TaskGroup) string {
	for i := range group.Results {
		result := group.Results[i]
		if result.FailureReason != nil && *result.FailureReason != "" {
			return *result.FailureReason
		}
		if result.ErrorMessage != nil && *result.ErrorMessage != "" {
			return *result.ErrorMessage
		}
	}
	return "unknown failure"
}

func isFishNetworkErr(err error) bool {
	var requestErr *fish.RequestError
	return errors.As(err, &requestErr) && requestErr.IsNetwork()
}

func isFishTransientErr(err error) bool {
	var rateLimited *fish.RateLimitedError
	if errors.As(err, &rateLimited) {
		return true
	}
	var temporary *fish.UpstreamTemporaryError
	if errors.As(err, &temporary) {
		return true
	}
	return isFishNetworkErr(err)
}

// ── 包内错误类型（分类器消费） ──

// fishSubmitIndeterminateError 标记「提交结果不明」：上游可能已受理并
// 扣积分，调用方不得重试或换号。
type fishSubmitIndeterminateError struct{ err error }

func (e *fishSubmitIndeterminateError) Error() string {
	return fmt.Sprintf("fish submit indeterminate: %v", e.err)
}

func (e *fishSubmitIndeterminateError) Unwrap() error { return e.err }

// fishGenerationFailedError 是上游显式失败（积分已退，可换账号重试）。
type fishGenerationFailedError struct {
	groupID string
	reason  string
}

func (e *fishGenerationFailedError) Error() string {
	return fmt.Sprintf("fish generation %s failed: %s", e.groupID, e.reason)
}

// fishResultUnavailableError 是任务已受理但结果取不回（轮询超时/下载
// 失败）。不重提（可能双扣），客户端按上游故障处理。
type fishResultUnavailableError struct {
	groupID string
	err     error
}

func (e *fishResultUnavailableError) Error() string {
	return fmt.Sprintf("fish generation %s result unavailable: %v", e.groupID, e.err)
}

func (e *fishResultUnavailableError) Unwrap() error { return e.err }
