package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// bpsSchedulingSettingRepo also answers the single-key reads the scheduling
// path makes (quota auto-pause settings) besides the BPS switch.
type bpsSchedulingSettingRepo struct {
	excelBPSSwitchSettingRepo
}

func (r *bpsSchedulingSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func bpsDisabledSchedulingSettingService() *SettingService {
	return NewSettingService(&bpsSchedulingSettingRepo{excelBPSSwitchSettingRepo{values: map[string]string{SettingKeyExcelBPSEnabled: "false"}}}, &config.Config{})
}

func bpsRoutingNativeAccount(id int64, priority int) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: priority,
		Credentials: map[string]any{"access_token": "native-token"}}
}

func bpsRoutingBPSAccount(id int64, models ...string) Account {
	account := *excelAccount()
	account.ID = id
	account.Priority = 0
	if len(models) > 0 {
		account.Extra = map[string]any{"openai_excel_bps_models": models}
	}
	return account
}

// BPS has no token-counting endpoint: an Excel-client token must never reach
// the native api.openai.com/v1/responses/input_tokens preflight.
func TestForwardResponsesInputTokensBPSOAuthUsesLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}

	err := svc.ForwardResponsesInputTokens(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","input":"hello world"}`))

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq)
	require.True(t, shouldEstimateOpenAIInputTokensLocally(excelAccount()))
}

func TestForwardCountTokensAsAnthropicBPSOAuthUsesLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello world"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, excelAccount(), body, "gpt-5.6-sol")

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq)
}

// The legacy scheduler applies the same BPS-only rules as the advanced one:
// entry points without a BPS route, the BPS model whitelist and the global
// switch all keep a BPS-only account out, while model-less listing still
// reaches it.
func TestLegacySelectionAppliesBPSOAuthRules(t *testing.T) {
	newSvc := func(accounts ...Account) *OpenAIGatewayService {
		return &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: accounts}, cache: &stubGatewayCache{}, cfg: &config.Config{}}
	}
	ctx := context.Background()

	t.Run("BPS preferred on the Responses entry point", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1))
		account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.6-sol", nil)
		require.NoError(t, err)
		require.Equal(t, int64(1), account.ID)
	})

	t.Run("entry point without a BPS route", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1))
		account, err := svc.SelectAccountForModelWithExclusions(WithBPSUnsupportedInbound(ctx), nil, "", "gpt-5.6-sol", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), account.ID)
	})

	t.Run("token count on a marked entry point", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1))
		account, err := svc.SelectAccountForTokenCount(WithBPSUnsupportedInbound(ctx), nil, "", "gpt-5.6-sol", OpenAIEndpointCapabilityChatCompletions, PlatformOpenAI)
		require.NoError(t, err)
		require.Equal(t, int64(2), account.ID)
	})

	t.Run("model outside the BPS whitelist", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1, "gpt-7-new"))
		account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.6-sol", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), account.ID)
	})

	t.Run("model-less listing keeps a whitelisted BPS account", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1, "gpt-7-new"))
		account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "", nil)
		require.NoError(t, err)
		require.Equal(t, int64(1), account.ID)
	})

	t.Run("global switch off", func(t *testing.T) {
		svc := newSvc(bpsRoutingNativeAccount(2, 5), bpsRoutingBPSAccount(1))
		svc.settingService = bpsDisabledSchedulingSettingService()
		account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.6-sol", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), account.ID)
	})
}

// bpsCooldownCases cool the BPS candidate down in the two ways the candidate
// pool does not filter: the account's BPS 429 cooldown and the model
// permission cooldown.
var bpsCooldownCases = []struct {
	name string
	cool func(svc *OpenAIGatewayService, bps *Account)
}{
	{name: "BPS 429 cooldown", cool: func(svc *OpenAIGatewayService, bps *Account) {
		svc.coolDownExcelBPS(context.Background(), bps, "60")
	}},
	{name: "model permission cooldown", cool: func(svc *OpenAIGatewayService, bps *Account) {
		svc.coolDownExcelBPSModel(bps, "gpt-5.6-sol")
	}},
}

// A native sticky binding yields to BPS only when a BPS account can and will
// serve this request: Chat Completions, Messages and count_tokens keep it, and
// so does every request while the BPS candidate is cooling down.
func TestLegacyStickyBindingYieldsToBPSOnlyWhenBPSCanServe(t *testing.T) {
	const sessionHash = "bps-sticky"
	type stickyCase struct {
		name      string
		ctx       context.Context
		cool      func(svc *OpenAIGatewayService, bps *Account)
		wantID    int64
		wantEvict bool
	}
	cases := []stickyCase{
		{name: "Responses", ctx: context.Background(), wantID: 1, wantEvict: true},
		{name: "unsupported entry point", ctx: WithBPSUnsupportedInbound(context.Background()), wantID: 2, wantEvict: false},
	}
	for _, cooldown := range bpsCooldownCases {
		cases = append(cases, stickyCase{name: cooldown.name, ctx: context.Background(), cool: cooldown.cool, wantID: 2, wantEvict: false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bps := bpsRoutingBPSAccount(1)
			cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + sessionHash: 2}}
			svc := &OpenAIGatewayService{
				accountRepo: stubOpenAIAccountRepo{accounts: []Account{bpsRoutingNativeAccount(2, 5), bpsRoutingNativeAccount(3, 5), bps}},
				cache:       cache,
				cfg:         &config.Config{},
			}
			if tc.cool != nil {
				tc.cool(svc, &bps)
			}

			account, err := svc.SelectAccountForModelWithExclusions(tc.ctx, nil, sessionHash, "gpt-5.6-sol", nil)

			require.NoError(t, err)
			require.Equal(t, tc.wantID, account.ID)
			require.Equal(t, tc.wantEvict, cache.deletedSessions["openai:"+sessionHash] > 0)
		})
	}
}

func TestStickyShouldYieldToBPS(t *testing.T) {
	native := bpsRoutingNativeAccount(2, 5)
	scoped := bpsRoutingBPSAccount(1, "gpt-7-new")
	ctx := context.Background()
	schedulable := func(*Account) bool { return true }

	require.True(t, stickyShouldYieldToBPS(ctx, &native, []Account{native, scoped}, "gpt-7-new", OpenAIEndpointCapabilityResponses, "", schedulable))
	require.False(t, stickyShouldYieldToBPS(ctx, &native, []Account{native, scoped}, "gpt-5.6-sol", OpenAIEndpointCapabilityResponses, "", schedulable), "outside the BPS whitelist")
	require.False(t, stickyShouldYieldToBPS(WithBPSUnsupportedInbound(ctx), &native, []Account{native, scoped}, "gpt-7-new", "", "", schedulable), "no BPS route for this entry point")
	require.False(t, stickyShouldYieldToBPS(WithBPSRouteExcluded(ctx), &native, []Account{native, scoped}, "gpt-7-new", OpenAIEndpointCapabilityResponses, "", schedulable), "BPS cannot carry this request")
	require.False(t, stickyShouldYieldToBPS(ctx, &native, []Account{native, scoped}, "gpt-7-new", OpenAIEndpointCapabilityEmbeddings, "", schedulable))
	require.False(t, stickyShouldYieldToBPS(ctx, &native, []Account{native}, "gpt-7-new", "", "", schedulable), "no BPS candidate")
	require.False(t, stickyShouldYieldToBPS(ctx, &scoped, []Account{native, scoped}, "gpt-7-new", "", "", schedulable), "already on BPS")
	require.False(t, stickyShouldYieldToBPS(ctx, &native, []Account{native, scoped}, "gpt-7-new", "", "", func(*Account) bool { return false }), "BPS candidate not schedulable")
}

// Forward-time backstop: a BPS-only account that cannot serve the request
// hands it to another account instead of answering 400.
func TestForwardBPSOAuthRouteUnavailableFailsOver(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *Account
		svc     func(*httpUpstreamRecorder) *OpenAIGatewayService
	}{
		{name: "global switch off", account: excelAccount(), svc: func(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
			svc := openAIClientToolsTestService(upstream)
			svc.settingService = excelBPSDisabledSettingService()
			return svc
		}},
		{name: "model outside the whitelist", account: func() *Account {
			account := bpsRoutingBPSAccount(300, "gpt-7-new")
			return &account
		}(), svc: openAIClientToolsTestService},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			upstream := &httpUpstreamRecorder{}
			request, err := buildExcelBPSAccountTestBody("gpt-5.6-sol", "hi")
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(request))

			_, err = tc.svc(upstream).Forward(context.Background(), c, tc.account, request)

			var failover *UpstreamFailoverError
			require.True(t, errors.As(err, &failover))
			require.Equal(t, ExcelOAuthRouteUnavailableReason, failover.Reason)
			require.True(t, failover.ShouldRetryNextAccount())
			require.Empty(t, upstream.requests)
			require.Zero(t, rec.Body.Len(), "leave the response to the handler")
		})
	}
}

// The advanced scheduler's sticky layer follows the same entry-point rule.
func TestAdvancedStickyBindingYieldsToBPSOnlyWhenBPSCanServe(t *testing.T) {
	groupID := int64(10)
	native := bpsRoutingNativeAccount(2001, 5)
	native.GroupIDs = []int64{groupID}
	bps := bpsRoutingBPSAccount(2002)
	bps.GroupIDs = []int64{groupID}
	type stickyCase struct {
		name       string
		ctx        context.Context
		cool       func(svc *OpenAIGatewayService, bps *Account)
		wantID     int64
		wantSticky bool
	}
	cases := []stickyCase{
		{name: "Responses", ctx: context.Background(), wantID: bps.ID, wantSticky: false},
		{name: "unsupported entry point", ctx: WithBPSUnsupportedInbound(context.Background()), wantID: native.ID, wantSticky: true},
	}
	for _, cooldown := range bpsCooldownCases {
		cases = append(cases, stickyCase{name: cooldown.name, ctx: context.Background(), cool: cooldown.cool, wantID: native.ID, wantSticky: true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:bps_sticky": native.ID}}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{native, bps}},
				cache:              cache,
				cfg:                &config.Config{},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			if tc.cool != nil {
				tc.cool(svc, &bps)
			}

			selection, decision, err := svc.SelectAccountWithScheduler(tc.ctx, &groupID, "", "bps_sticky", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)

			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, tc.wantID, selection.Account.ID)
			require.Equal(t, tc.wantSticky, decision.StickySessionHit)
			require.Equal(t, !tc.wantSticky, cache.deletedSessions["openai:bps_sticky"] > 0)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

// A sticky binding to a BPS account must not bypass that account's in-memory
// BPS cooldowns: the session moves to another account while they last.
func TestLegacyStickyBindingToCoolingBPSAccountMoves(t *testing.T) {
	const sessionHash = "bps-cooling-sticky"
	for _, cooldown := range bpsCooldownCases {
		t.Run(cooldown.name, func(t *testing.T) {
			bps := bpsRoutingBPSAccount(1)
			cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + sessionHash: bps.ID}}
			svc := &OpenAIGatewayService{
				accountRepo: stubOpenAIAccountRepo{accounts: []Account{bps, bpsRoutingNativeAccount(2, 5)}},
				cache:       cache,
				cfg:         &config.Config{},
			}
			cooldown.cool(svc, &bps)

			account, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, sessionHash, "gpt-5.6-sol", nil)

			require.NoError(t, err)
			require.Equal(t, int64(2), account.ID)
		})
	}
}

func TestAdvancedStickyBindingToCoolingBPSAccountMoves(t *testing.T) {
	groupID := int64(11)
	native := bpsRoutingNativeAccount(2101, 5)
	native.GroupIDs = []int64{groupID}
	bps := bpsRoutingBPSAccount(2102)
	bps.GroupIDs = []int64{groupID}
	for _, cooldown := range bpsCooldownCases {
		t.Run(cooldown.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:bps_cooling": bps.ID}}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{native, bps}},
				cache:              cache,
				cfg:                &config.Config{},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			cooldown.cool(svc, &bps)

			selection, decision, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "bps_cooling", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)

			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, native.ID, selection.Account.ID)
			require.False(t, decision.StickySessionHit)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

// countingExcelBPSSwitchRepo counts the switch reads the scheduler makes.
type countingExcelBPSSwitchRepo struct {
	bpsSchedulingSettingRepo
	reads int
}

func (r *countingExcelBPSSwitchRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	for _, key := range keys {
		if key == SettingKeyExcelBPSEnabled {
			r.reads++
		}
	}
	return r.bpsSchedulingSettingRepo.GetMultiple(ctx, keys)
}

// Every BPS candidate consults the global switch; the database is read once.
func TestSchedulerReadsExcelBPSSwitchOncePerPool(t *testing.T) {
	repo := &countingExcelBPSSwitchRepo{bpsSchedulingSettingRepo: bpsSchedulingSettingRepo{excelBPSSwitchSettingRepo{values: map[string]string{}}}}
	svc := &OpenAIGatewayService{
		accountRepo:    stubOpenAIAccountRepo{accounts: []Account{bpsRoutingBPSAccount(1), bpsRoutingBPSAccount(2), bpsRoutingBPSAccount(3), bpsRoutingNativeAccount(4, 5)}},
		cache:          &stubGatewayCache{},
		cfg:            &config.Config{},
		settingService: NewSettingService(repo, &config.Config{}),
	}
	for i := 0; i < 3; i++ {
		account, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.6-sol", nil)
		require.NoError(t, err)
		require.True(t, account.IsBPSOAuth())
	}
	require.Equal(t, 1, repo.reads)
}

// The route check and the BPS dispatch share one read of the switch, and a
// BPS-only account that may not use BPS never reaches the native path.
func TestForwardBPSOAuthReadsSwitchOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &countingExcelBPSSwitchRepo{bpsSchedulingSettingRepo: bpsSchedulingSettingRepo{excelBPSSwitchSettingRepo{values: map[string]string{SettingKeyExcelBPSEnabled: "false"}}}}
	upstream := &httpUpstreamRecorder{}
	svc := openAIClientToolsTestService(upstream)
	svc.settingService = NewSettingService(repo, &config.Config{})
	request, err := buildExcelBPSAccountTestBody("gpt-5.6-sol", "hi")
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(request))

	_, err = svc.Forward(context.Background(), c, excelAccount(), request)

	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, ExcelOAuthRouteUnavailableReason, failover.Reason)
	require.Equal(t, 1, repo.reads)
	require.Empty(t, upstream.requests)
}
