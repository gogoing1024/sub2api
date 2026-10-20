package domain

import (
	"fmt"
	"strings"
)

// FishSizeMode 描述 fish.audio 一个模型的尺寸参数语义（2026-10 抓包 +
// price Oracle 穷举确认）：
//
//   - FishSizePixels：gpt-image 家族。params.image_size 用固定像素串，
//     仅允许 FishPixelSizes 列出的 12 个值；quality 三档（low/medium/high）。
//   - FishSizeTier：nano-banana / gemini 系。image_size 用档位字符串
//     "1K"/"2K"/"4K"（2-lite 仅 "1K"），与宽高比无关；quality 不参与。
//   - FishSizeFree：seedream 系。任意尺寸一口价，像素串原样透传。
type FishSizeMode string

const (
	FishSizePixels FishSizeMode = "pixels"
	FishSizeTier   FishSizeMode = "tier"
	FishSizeFree   FishSizeMode = "free"
)

// FishModel 是 fish.audio 平台的一个对外模型。Name 同时是上游 modelId
//（fish 上游模型名即对外名，无需 slug 换算）。
type FishModel struct {
	Name     string
	SizeMode FishSizeMode
	// TierOnly 对 FishSizeTier 模型列出允许的档位（如 nano-banana-2-lite 仅 1K）。
	Tiers []string
}

// FishModels 是 fish.audio 平台首版暴露的生图模型（展示顺序）。
// grok-imagine-image（费率键未探明）与 topaz/image/enhance（scaleFactor
// 参数体系）不在首版范围。列表是 2026-10-20 实测快照；fish 的 /models
// 端点是活的，账号连通性测试会顺带校验漂移。
var FishModels = []FishModel{
	{Name: "gpt-image-2", SizeMode: FishSizePixels},
	{Name: "gpt-image-2.5-flare", SizeMode: FishSizePixels},
	{Name: "gpt-image-2.5-sunburst", SizeMode: FishSizePixels},
	{Name: "nano-banana", SizeMode: FishSizeTier, Tiers: []string{"1K", "2K", "4K"}},
	{Name: "nano-banana-2-lite", SizeMode: FishSizeTier, Tiers: []string{"1K"}},
	{Name: "nano-banana-2-1", SizeMode: FishSizeTier, Tiers: []string{"1K", "2K", "4K"}},
	{Name: "gemini-3-pro-image", SizeMode: FishSizeTier, Tiers: []string{"1K", "2K", "4K"}},
	{Name: "seedream-4-5", SizeMode: FishSizeFree},
	{Name: "seedream-5-0", SizeMode: FishSizeFree},
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
// 矩阵不对称：21:9 只有横版，4K 只有 1:1）。
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

// fishTierByPixels 把像素面积映射到档位字符串（Gemini 系模型的
// OpenAI size → fish 档位换算）。面积阈值按 gpt 家族矩阵反推：
// 1K 档上限 ~1.1MP（1024²），2K 档上限 ~4.3MP（2048²），其余归 4K。
func fishTierByPixels(width, height int) string {
	pixels := width * height
	switch {
	case pixels <= 1_200_000:
		return "1K"
	case pixels <= 4_400_000:
		return "2K"
	default:
		return "4K"
	}
}

// ParsePixelSize 把 "WxH" 解析为宽高。
func ParsePixelSize(size string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(strings.ToLower(size)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	var width, height int
	if _, err := fmt.Sscanf(parts[0], "%d", &width); err != nil {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &height); err != nil {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	return width, height, nil
}

// FishResolveSize 把 OpenAI Images 请求的 size（如 "1024x1024"；
// 空串用默认）翻译为 fish params.image_size 的值。返回的 qualityAllowed
// 报告该模型是否接受 quality 参数（仅 gpt 家族）。
func FishResolveSize(model FishModel, size string) (imageSize string, qualityAllowed bool, err error) {
	size = strings.TrimSpace(strings.ToLower(size))
	switch model.SizeMode {
	case FishSizePixels:
		if size == "" {
			size = "1024x1024"
		}
		if !fishPixelSizeSet[size] {
			return "", false, fmt.Errorf(
				"model %s does not support size %q; allowed: %s",
				model.Name, size, strings.Join(FishPixelSizes, ", "),
			)
		}
		return size, true, nil
	case FishSizeTier:
		if size == "" {
			tier := "1K"
			if len(model.Tiers) > 0 {
				tier = model.Tiers[0]
			}
			return tier, false, nil
		}
		// 档位串直接用（大小写归一），像素串按面积换算
		normalized := strings.ToUpper(size)
		for _, tier := range model.Tiers {
			if tier == normalized {
				return tier, false, nil
			}
		}
		width, height, perr := ParsePixelSize(size)
		if perr != nil {
			return "", false, fmt.Errorf("invalid image size %q", size)
		}
		tier := fishTierByPixels(width, height)
		for _, allowed := range model.Tiers {
			if allowed == tier {
				return tier, false, nil
			}
		}
		return "", false, fmt.Errorf(
			"model %s resolves size %q to tier %s which it does not support (allowed: %s)",
			model.Name, size, tier, strings.Join(model.Tiers, ", "),
		)
	case FishSizeFree:
		if size == "" {
			return "1024x1024", false, nil
		}
		width, height, perr := ParsePixelSize(size)
		if perr != nil {
			return "", false, fmt.Errorf("invalid image size %q", size)
		}
		return fmt.Sprintf("%dx%d", width, height), false, nil
	}
	return "", false, fmt.Errorf("unknown size mode for model %s", model.Name)
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
