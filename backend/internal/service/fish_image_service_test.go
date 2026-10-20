package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// newFishTestService 构造指向假上游的服务与账号。
func newFishTestService(t *testing.T, handler http.Handler) (*FishImageService, *Account, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	service := NewFishImageService(nil)
	service.clients.options = fish.Options{APIBase: server.URL}
	account := &Account{
		ID:     1,
		Type:   AccountTypeAPIKey,
		Credentials: map[string]any{
			fishCredentialToken:    "tok",
			fishCredentialTeamID:   "team-1",
			fishCredentialDeviceID: "dev-1",
		},
	}
	return service, account, server
}

func fishImageRequest(model, size, quality string, n int) *OpenAIImagesRequest {
	return &OpenAIImagesRequest{Model: model, Prompt: "a cat", Size: size, Quality: quality, N: n}
}

func TestFishGenerateCallHappyPath(t *testing.T) {
	pngBytes := []byte("pngdata")
	var generateBody map[string]any
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
		case "/tools/image-video/generate":
			generateBody = decodeFishBody(t, r)
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
		case "/task/group/grp-1":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
		case "/img.png":
			_, _ = w.Write(pngBytes)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "medium", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.GroupID != "grp-1" {
		t.Fatalf("GroupID = %q", result.GroupID)
	}
	// 请求体：像素尺寸 + quality + 计价回显
	params := generateBody["params"].(map[string]any)
	if params["image_size"] != "1024x1024" || params["quality"] != "medium" {
		t.Fatalf("params = %v", params)
	}
	if generateBody["expectedCredits"] != float64(650) || generateBody["pricingVersion"] != "pv-1" {
		t.Fatalf("pricing echo = %v", generateBody)
	}
	// 响应体：单张 b64
	var bodyMap map[string]any
	if err := json.Unmarshal(result.Body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	data := bodyMap["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data len = %d", len(data))
	}
	b64 := data[0].(map[string]any)["b64_json"].(string)
	if decoded, _ := base64.StdEncoding.DecodeString(b64); string(decoded) != string(pngBytes) {
		t.Fatalf("b64 mismatch")
	}
	// 计费转发：1K 档
	if result.Forward.ImageCount != 1 || result.Forward.ImageSize != ImageBillingSize1K {
		t.Fatalf("Forward = %+v", result.Forward)
	}
}

func TestFishGenerateCallEditsFlow(t *testing.T) {
	png := []byte("ref-image")
	var variant string
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/upload-url":
			_, _ = w.Write([]byte(`{"object_key":"media_gen_ref/x/image.png","upload_url":"http://` + r.Host + `/r2"}`))
		case "/r2":
			w.WriteHeader(200)
		case "/tools/image-video/media/inspect":
			_, _ = w.Write([]byte(`{"objectKey":"media_gen_ref/x/image.png","width":512,"height":512}`))
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":1820,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			body := decodeFishBody(t, r)
			variant = body["variantKey"].(string)
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			_, _ = w.Write([]byte(`{"id":"g","status":"done","results":[{"id":"m","status":"done","mediaItem":{"id":"m","url":"http://` + r.Host + `/o.png"}}]}`))
		case "/o.png":
			_, _ = w.Write([]byte("out"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	req := fishImageRequest("gpt-image-2", "2048x2048", "medium", 1)
	req.Uploads = []OpenAIImagesUpload{{Data: png, ContentType: "image/png"}}
	if _, err := service.Generate(context.Background(), account, "tok", req); err != nil {
		t.Fatalf("Generate(edits): %v", err)
	}
	if variant != fish.VariantEdit {
		t.Fatalf("variant = %q, want edit", variant)
	}
}

func TestFishGenerateCallIndeterminateSubmit(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			// 模拟提交阶段连接被掐断：结果不明
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server does not support hijack")
			}
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close()
		default:
			w.WriteHeader(404)
		}
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "", 1))
	var indeterminate *fishSubmitIndeterminateError
	if !errors.As(err, &indeterminate) {
		t.Fatalf("err = %v, want fishSubmitIndeterminateError", err)
	}
}

func TestFishGenerateCallPollTransientRetry(t *testing.T) {
	// 恢复间隔缩放
	oldInitial, oldMax := fishPollInitialInterval, fishPollMaxInterval
	fishPollInitialInterval, fishPollMaxInterval = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { fishPollInitialInterval, fishPollMaxInterval = oldInitial, oldMax })

	var pollCount int32
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			if atomic.AddInt32(&pollCount, 1) <= 2 {
				w.WriteHeader(http.StatusBadGateway) // 暂态
				return
			}
			_, _ = w.Write([]byte(`{"id":"g","status":"done","results":[{"id":"m","status":"done","mediaItem":{"id":"m","url":"http://` + r.Host + `/i.png"}}]}`))
		case "/i.png":
			_, _ = w.Write([]byte("img"))
		default:
			w.WriteHeader(404)
		}
	}))
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "", "", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if pollCount < 3 {
		t.Fatalf("pollCount = %d, want >=3 (transient retried)", pollCount)
	}
	if result.Forward.ImageCount != 1 {
		t.Fatalf("ImageCount = %d", result.Forward.ImageCount)
	}
}

func TestFishGenerateCallFailedGroup(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			_, _ = w.Write([]byte(`{"id":"g","status":"failed","results":[{"id":"m","status":"failed","failureReason":"moderation"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "", "", 1))
	var failed *fishGenerationFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want fishGenerationFailedError", err)
	}
	if failed.reason != "moderation" {
		t.Fatalf("reason = %q", failed.reason)
	}
}

func TestFishGenerateCallUnsupportedSizeIsRequestError(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream should not be called for invalid size")
		w.WriteHeader(404)
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "123x456", "", 1))
	if err == nil {
		t.Fatal("expected error")
	}
	failure := classifyFishError(err)
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("classify = %+v", failure.Failover)
	}
}

func decodeFishBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestFishQualityForRequest(t *testing.T) {
	cases := []struct {
		requested string
		want      string
	}{
		{"medium", "medium"},
		{"standard", "medium"},
		{"hd", "high"},
		{"high", "high"},
		{"low", "low"},
		{"", "medium"},
		{"auto", "medium"},
	}
	for _, tc := range cases {
		if got := fishQualityForRequest(tc.requested); got != tc.want {
			t.Errorf("fishQualityForRequest(%q) = %q, want %q", tc.requested, got, tc.want)
		}
	}
}
