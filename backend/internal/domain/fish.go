package domain

import (
	"fmt"
	"strings"
)

// FishModel 是 fish.audio 平台的一个对外模型。Name 同时是上游 modelId
//（fish 上游模型名即对外名）。首版只暴露 gpt-image 家族——全部走固定像素
// 白名单（FishPixelSizes）+ quality 三档（low/medium/high）。
type FishModel struct {
	Name string
}

// FishModels 是 fish.audio 平台暴露的生图模型（展示顺序）。
// nano-banana / gemini / seedream 系已按需求移除（2026-10）；
// fish 的 /models 端点是活的，账号连通性测试可顺带校验漂移。
var FishModels = []FishModel{
	{Name: "gpt-image-2"},
	{Name: "gpt-image-2.5-flare"},
	{Name: "gpt-image-2.5-sunburst"},
}

// DefaultFishModelMapping 是 fish 账号的默认模型映射：对外名恒等映射。
var DefaultFishModelMapping = func() map[string]string {
	mapping := make(map[string]string, len(FishModels))
	for _, model := range FishModels {
		mapping[model.Name] = model.Name
	}
	return mapping
}()

// FishPixelSizes 是 gpt-image 家族（gpt-image-2 / 2.5-flare / 2.5-sunburst）
// 的全部合法像素值（三家共享同一矩阵，2026-10-20 price Oracle 实测；
// 矩阵不对称：21:9 只有横版，4K 只有 1:1）。白名单外的尺寸由 handler
// 路由到 OpenAI 兼容中转账号。
var FishPixelSizes = []string{
	// 1K 档
	"1024x1024", "768x1024", "1024x768", "608x1088", "1088x608", "1344x576",
	// 2K 档
	"2048x2048", "1536x2048", "2048x1536", "1152x2048", "2048x1152",
	// 4K 档
	"2880x2880",
}

var fishPixelSizeSet = func() map[string]bool {
	set := make(map[string]bool, len(FishPixelSizes))
	for _, size := range FishPixelSizes {
		set[size] = true
	}
	return set
}()

// FishResolveSize 把 OpenAI Images 请求的 size（空串用默认 1024x1024）
// 翻译为 fish params.image_size 的值。白名单外的尺寸返回错误——调用方
//（handler）据此把请求路由到中转账号，而不是打 fish 原生。
func FishResolveSize(model FishModel, size string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(size))
	if normalized == "" {
		normalized = "1024x1024"
	}
	if !fishPixelSizeSet[normalized] {
		return "", fmt.Errorf(
			"model %s does not support size %q natively; allowed: %s",
			model.Name, size, strings.Join(FishPixelSizes, ", "),
		)
	}
	return normalized, nil
}

// FishSizeSupported 报告 size 是否在 fish 原生白名单内（空串视为默认尺寸，
// 恒支持）。handler 的中转判定用。
func FishSizeSupported(size string) bool {
	normalized := strings.TrimSpace(strings.ToLower(size))
	if normalized == "" {
		return true
	}
	return fishPixelSizeSet[normalized]
}

// LookupFishModel 按模型名查表（大小写不敏感）。
func LookupFishModel(model string) (FishModel, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return FishModel{}, false
	}
	for _, candidate := range FishModels {
		if strings.EqualFold(candidate.Name, model) {
			return candidate, true
		}
	}
	return FishModel{}, false
}

// IsFishPublicModel 报告 model 是否为 fish 平台的对外模型名。
func IsFishPublicModel(model string) bool {
	_, ok := LookupFishModel(model)
	return ok
}

// FishModelNames 返回对外名列表（展示顺序）。
func FishModelNames() []string {
	names := make([]string, 0, len(FishModels))
	for _, model := range FishModels {
		names = append(names, model.Name)
	}
	return names
}
