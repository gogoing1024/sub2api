package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/gin-gonic/gin"
)

// TestFishAccountTestConnectionBackfillsIdentity 断言连通性测试把
// team_id / device_id 回填进凭据（token 原样往返）。
func TestFishAccountTestConnectionBackfillsIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var persisted map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/self":
			_, _ = w.Write([]byte(`{"_id":"uid-9","email":"a@b.c","nickname":"n","banned":false}`))
		case "/wallet/uid-9/package":
			_, _ = w.Write([]byte(`{"total":8000,"balance":8000,"type":"free"}`))
		case "/tools/image-video/entitlements":
			_, _ = w.Write([]byte(`{"plan":"free","maxImagesPerGeneration":2,"videoAllowed":false}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)

	svc := &AccountTestService{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	account := &Account{
		ID:          42,
		Type:        AccountTypeAPIKey,
		Platform:    PlatformFish,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}
	// 直接替换 account.Credentials 观察回填：persistAccountCredentials 会写
	// account.Credentials 再尝试 repo（repo 为 nil 时跳过落库）
	err := svc.testFishAccountConnectionWithBase(c, account, server.URL)
	if err != nil {
		t.Fatalf("testFishAccountConnection: %v", err)
	}
	_ = persisted
	// repo 为 nil 时凭据仍更新到内存 account 上
	if account.GetCredential(fishCredentialTeamID) != "uid-9" {
		t.Fatalf("team_id = %q, want uid-9", account.GetCredential(fishCredentialTeamID))
	}
	if account.GetCredential(fishCredentialDeviceID) == "" {
		t.Fatal("device_id not backfilled")
	}
	if account.GetCredential(fishCredentialToken) != "tok" {
		t.Fatalf("token must round-trip unchanged")
	}
	_ = fish.Identity{}
}
