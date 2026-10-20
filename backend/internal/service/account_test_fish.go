package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/gin-gonic/gin"
)

// fishTestTimeout 是 fish 账号连通性测试的整体预算（不生成图片）。
const fishTestTimeout = 30 * time.Second

// testFishAccountConnection 验证 fish 账号 token 并回填 team_id / device_id
//（指纹身份持久化；上游已验证这两个头可省，回填只为更完整的浏览器指纹）。
// 不做真实生成——fish 免费额度每日仅 3 张，测试出图太贵。
func (s *AccountTestService) testFishAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	if isFishRelayAccount(account) {
		testModel := strings.TrimSpace(modelID)
		if testModel == "" {
			if ids := FishModelIDs(); len(ids) > 0 {
				testModel = ids[0]
			}
		}
		return s.testOpenAIImageAPIKey(c, c.Request.Context(), account, account.GetMappedModel(testModel), resolveFishTestPrompt(prompt))
	}
	return s.testFishAccountConnectionWithBase(c, account, "")
}

// resolveFishTestPrompt 补默认测试提示词（中转号连通性测试用）。
func resolveFishTestPrompt(prompt string) string {
	if strings.TrimSpace(prompt) != "" {
		return prompt
	}
	return "a red cube on a white background"
}

func (s *AccountTestService) testFishAccountConnectionWithBase(c *gin.Context, account *Account, apiBase string) error {
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "fish /user/self"})

	token := strings.TrimSpace(account.GetCredential(fishCredentialToken))
	if token == "" {
		return s.sendErrorAndEnd(c, "fish account has no token — paste the Bearer token from a logged-in fish.audio session (localStorage `token`)")
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), fishTestTimeout)
	defer cancel()

	// device_id 缺失时生成一个并随 team_id 一起回填（CAS：token 未变才写）
	deviceID := strings.TrimSpace(account.GetCredential(fishCredentialDeviceID))
	if deviceID == "" {
		deviceID = fish.NewDeviceID()
	}
	identity := fish.Identity{DeviceID: deviceID}

	client := fish.NewClient(fish.Options{ProxyURL: accountProxyURL(account), APIBase: apiBase})
	self, err := client.UserSelf(ctx, token, identity)
	if err != nil {
		return s.sendErrorAndEnd(c, formatFishTestError(err))
	}
	if self.Banned {
		return s.sendErrorAndEnd(c, "fish account is banned")
	}

	display := self.Email
	if display == "" {
		display = self.ID
	}
	s.sendEvent(c, TestEvent{Type: "status", Text: fmt.Sprintf("Account: %s", display)})

	wallet, err := client.QueryWallet(ctx, token, identity, self.ID)
	if err == nil && wallet != nil {
		s.sendEvent(c, TestEvent{Type: "status",
			Text: fmt.Sprintf("Credits: %d / %d (%s)", wallet.Balance, wallet.Total, wallet.Type)})
	}
	if entitlements, err := client.QueryEntitlements(ctx, token, identity); err == nil && entitlements != nil {
		s.sendEvent(c, TestEvent{Type: "status",
			Text: fmt.Sprintf("Plan: %s, max images per generation: %d", entitlements.Plan, entitlements.MaxImagesPerGeneration)})
	}

	if account.ID > 0 {
		credentials := shallowCopyMap(account.Credentials)
		credentials[fishCredentialToken] = token
		credentials[fishCredentialTeamID] = self.ID
		credentials[fishCredentialDeviceID] = deviceID
		// 内存态先更新（repo 缺失时 persistAccountCredentials 直接跳过），
		// 再走标准持久化。
		account.Credentials = credentials
		if updateErr := persistAccountCredentials(ctx, s.accountRepo, account, credentials); updateErr != nil {
			// 回填失败不阻断测试：身份头本就可省
			s.sendEvent(c, TestEvent{Type: "status", Text: "Note: failed to persist team_id/device_id (identity headers stay optional)"})
		}
	}

	s.sendEvent(c, TestEvent{Type: "test_complete", Status: "success"})
	return nil
}

// formatFishTestError 把 fish 客户端错误转成面向管理员的简短文案。
func formatFishTestError(err error) string {
	var authErr *fish.AuthError
	if errors.As(err, &authErr) {
		return "fish token was rejected (invalid token) — re-export a fresh token from a logged-in fish.audio session"
	}
	var rateLimited *fish.RateLimitedError
	if errors.As(err, &rateLimited) {
		return "fish upstream rate limited the test — retry in a moment"
	}
	return err.Error()
}
