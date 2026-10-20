package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// FishImageCall 跨账号切换保持的请求级状态（参考图只取一次）。
type FishImageCall struct {
	Request            *OpenAIImagesRequest
	ChannelMappedModel string
	inputsOnce         sync.Once
	inputs             []*fishInputImage
	inputsErr          error
}

// NewFishImageCall 构造一次调用的共享状态。
func NewFishImageCall(req *OpenAIImagesRequest, channelMappedModel string) *FishImageCall {
	return &FishImageCall{Request: req, ChannelMappedModel: channelMappedModel}
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

// GenerateCall 在指定账号上完成一次 fish 生图。
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
	count := parsed.N
	if count < 1 {
		count = 1
	}
	if count > fishMaxImagesPerRequest {
		return nil, fish.NewRequestError(fmt.Sprintf("fish: n=%d exceeds max %d per generation", count, fishMaxImagesPerRequest))
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return nil, fish.NewRequestError("fish: prompt is required")
	}

	client := s.clients.clientForAccount(account)
	identity := fishIdentityFromAccount(account)

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

	// 计价（同时校验参数组合合法性）
	price, err := client.Price(ctx, token, identity, fish.PriceParams{
		Model: model.Name, Variant: variant, ImageSize: imageSize,
		Quality: quality, Count: count, RefImageCount: len(refAssets),
	})
	if err != nil {
		return nil, err
	}

	// 提交（预算内）；网络错误 = 结果不明，绝不重试
	submitCtx, cancel := context.WithTimeout(ctx, fishSubmitBudget)
	group, err := client.SubmitGeneration(submitCtx, token, identity, fish.GenerateRequest{
		Model: model.Name, Variant: variant, Prompt: parsed.Prompt,
		Count: count, ImageSize: imageSize, Quality: quality,
		RefAssets: refAssets, ExpectedCredits: price.Credits, PricingVersion: price.PricingVersion,
	})
	cancel()
	if err != nil {
		if isFishNetworkErr(err) {
			return nil, &fishSubmitIndeterminateError{err: err}
		}
		return nil, err
	}

	// 轮询与下载脱离请求生命周期（客户端断开不打断）
	pollCtx, pollCancel := context.WithTimeout(context.WithoutCancel(ctx), fishPollTimeout)
	defer pollCancel()
	finalGroup, err := s.pollUntilDone(pollCtx, client, token, identity, group.ID)
	if err != nil {
		return nil, err
	}
	mediaItems := collectFishMedia(finalGroup)
	if len(mediaItems) == 0 {
		return nil, &fishResultUnavailableError{groupID: group.ID, err: fmt.Errorf("task finished without media")}
	}
	images := make([][]byte, 0, len(mediaItems))
	for _, item := range mediaItems {
		data, err := s.downloadWithRetry(pollCtx, client, item.URL)
		if err != nil {
			return nil, &fishResultUnavailableError{groupID: group.ID, err: err}
		}
		images = append(images, data)
	}

	body, err := s.buildResponseBody(ctx, images, imageSize, quality)
	if err != nil {
		return nil, err
	}
	return &FishImageResult{
		Body: body,
		Forward: &OpenAIForwardResult{
			Model:      parsed.Model,
			ImageCount: len(images),
			ImageSize:  fishBillingSize(imageSize),
		},
		GroupID: group.ID,
		Size:    imageSize,
		Quality: quality,
	}, nil
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
