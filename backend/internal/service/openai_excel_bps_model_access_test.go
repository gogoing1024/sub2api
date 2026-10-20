package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const bpsAccessFixture = `{"allowed":true,"model_catalog":{"restricted_models":["gpt-6-astra"],"models":[{"id":"gpt-6-astra"},{"id":"gpt-7-new","label":"New model","efforts":[{"value":"none"},{"value":"medium"},{"value":"high"},{"value":"high"}]},{"id":"gpt-hidden","model_picker_enabled":false},{"id":"gpt-policy-hidden","policy":{"state":"disabled"}}]}}`

func TestExcelBPSAccessCatalogFiltersPermissionsAndKeepsNewModels(t *testing.T) {
	body, err := excelBPSAccessModelsBody([]byte(bpsAccessFixture))
	require.NoError(t, err)
	require.Contains(t, string(body), `"id":"gpt-7-new"`)
	require.Contains(t, string(body), `"display_name":"New model"`)
	require.Contains(t, string(body), `"reasoning_efforts":["medium","high"]`)
	for _, id := range []string{"gpt-6-astra", "gpt-hidden", "gpt-policy-hidden"} {
		require.NotContains(t, string(body), id)
	}
	denied, err := excelBPSAccessModelsBody([]byte(`{"allowed":false}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"object":"list","data":[]}`, string(denied))
	for _, raw := range []string{`{}`, `{"allowed":true}`, `{"allowed":true,"model_catalog":{"models":null}}`, `{"allowed":"true"}`} {
		_, err := excelBPSAccessModelsBody([]byte(raw))
		require.Error(t, err)
	}
}

func TestExcelBPSAccessSharedRefreshSurvivesWaiterCancellation(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := svc.FetchExcelBPSModelsList(ctx, excelAccount()); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() { _, err := svc.FetchExcelBPSModelsList(context.Background(), excelAccount()); second <- err }()
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(release)
	require.NoError(t, <-second)
	require.EqualValues(t, 1, calls.Load())
	_, err := svc.FetchExcelBPSModelsList(ctx, excelAccount())
	require.ErrorIs(t, err, context.Canceled)
}

func TestExcelBPSAccessCacheCredentialIsolationExpiryAndErrors(t *testing.T) {
	var calls atomic.Int32
	reject := false
	upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/basispoints/api/responses/access", req.URL.Path)
		require.Equal(t, "true", req.URL.Query().Get("include_models"))
		require.Equal(t, "chatgpt", req.Header.Get("X-Basispoints-Auth-Mode"))
		require.Contains(t, req.Header.Get("Authorization"), "Bearer ")
		require.EqualValues(t, 300, accountID)
		if reject {
			response := ordinaryModelsUpstreamResponse(`{"error":"SYNTHETIC_SECRET"}`)
			response.StatusCode = 403
			return response, nil
		}
		return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
	}}
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = upstream
	account := excelAccount()
	first, err := svc.FetchExcelBPSModelsList(context.Background(), account)
	require.NoError(t, err)
	first.Body[0] = '!'
	second, err := svc.FetchExcelBPSModelsList(context.Background(), account)
	require.NoError(t, err)
	require.True(t, json.Valid(second.Body))
	require.EqualValues(t, 1, calls.Load())
	account.Credentials["access_token"] = "rotated-synthetic-token"
	_, err = svc.FetchExcelBPSModelsList(context.Background(), account)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	svc.excelBPSModelsCache.mu.Lock()
	for k, v := range svc.excelBPSModelsCache.entries {
		v.expiresAt = time.Now().Add(-time.Second)
		svc.excelBPSModelsCache.entries[k] = v
	}
	svc.excelBPSModelsCache.mu.Unlock()
	reject = true
	for range 2 {
		_, err = svc.FetchExcelBPSModelsList(context.Background(), account)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "SYNTHETIC_SECRET")
	}
	require.EqualValues(t, 4, calls.Load(), "rejections must not serve stale grants or be cached as permission")
	require.True(t, account.IsExcelBPSEnabled(), "catalog rejection must not disable business routes")
}

func TestExcelBPSDiscoveryProducesCodexEffortsAndMappedPicker(t *testing.T) {
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
	}}
	account := excelAccount()
	account.Extra["openai_passthrough"] = false
	account.Credentials["model_mapping"] = map[string]any{"public-new": "gpt-7-new"}
	manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "", "")
	require.NoError(t, err)
	require.Contains(t, string(manifest.Body), `"effort":"medium"`)
	require.Contains(t, string(manifest.Body), `"multi_agent_version":null`)
	notModified, err := svc.FetchCodexModelsManifest(context.Background(), account, "", manifest.ETag)
	require.NoError(t, err)
	require.True(t, notModified.NotModified)
	picker := &AccountTestService{openaiGatewayService: svc}
	models, err := picker.FetchOpenAIAccountModels(context.Background(), account)
	require.NoError(t, err)
	var found bool
	for _, m := range models {
		if m.ID == "public-new" {
			found = true
		}
		require.NotEqual(t, "gpt-6-astra", m.ID)
	}
	require.True(t, found)
}

type excelBPSShadowParentRepo struct {
	AccountRepository
	parent *Account
}

func (r excelBPSShadowParentRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.parent, nil
}

// A scoped Excel OAuth account has no native Codex channel: discovery must
// return only its BPS models and never dispatch back into itself. The old
// native merge went through the public dispatchers and recursed until the
// stack overflowed and killed the process.
func TestExcelBPSScopedDiscoveryReturnsBPSCatalogWithoutRecursion(t *testing.T) {
	var bpsCalls, nativeCalls atomic.Int32
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = excelBPSAccessOnlyUpstream(t, &bpsCalls)
	useNativeCodexManifestServer(t, &nativeCalls)
	account := excelAccount()
	account.Extra["openai_passthrough"] = false
	account.Extra["openai_excel_bps_models"] = []string{"gpt-7-new"}

	list, err := svc.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, string(list.Body), `"gpt-7-new"`)
	require.NotContains(t, string(list.Body), `"gpt-native"`)

	manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "", "")
	require.NoError(t, err)
	require.Contains(t, string(manifest.Body), `"gpt-7-new"`)
	require.NotContains(t, string(manifest.Body), `"gpt-native"`)
	require.Positive(t, bpsCalls.Load())
	require.Zero(t, nativeCalls.Load(), "Excel OAuth credentials must never reach native Codex discovery")
}

// Spark shadows never route through BPS, and BPS OAuth accounts cannot parent
// one: a malformed shadow pointing at a BPS-only parent must fail without
// sending the Excel token to ChatGPT or BPS.
func TestExcelBPSShadowOfExcelParentRefusesNativeCatalog(t *testing.T) {
	var bpsCalls, nativeCalls atomic.Int32
	svc := openAIClientToolsTestService(nil)
	svc.httpUpstream = excelBPSAccessOnlyUpstream(t, &bpsCalls)
	useNativeCodexManifestServer(t, &nativeCalls)
	parent := excelAccount()
	parent.Extra["openai_excel_bps_models"] = []string{"gpt-7-new"}
	svc.accountRepo = excelBPSShadowParentRepo{parent: parent}
	parentID := parent.ID
	shadow := &Account{ID: 301, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10,
		ParentAccountID: &parentID, Credentials: map[string]any{}, Extra: map[string]any{}}

	_, err := svc.FetchOpenAIModelsList(context.Background(), shadow)
	require.Error(t, err)
	require.Zero(t, bpsCalls.Load())
	require.Zero(t, nativeCalls.Load())
}

func TestExcelBPSMixedRoutesAndAliasesUseTheirOwnCatalog(t *testing.T) {
	account := excelAccount()
	account.Extra["openai_passthrough"] = false
	account.Extra["openai_excel_bps_models"] = []string{"gpt-7-new"}
	body, err := mergeExcelBPSAccountModels([]byte(`{"data":[{"id":"gpt-native"},{"id":"gpt-7-new"}]}`), []byte(`{"data":[{"id":"gpt-7-new","reasoning_efforts":["medium"]},{"id":"gpt-other"}]}`), account)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(body), `"id":"gpt-7-new"`))
	require.Contains(t, string(body), "gpt-native")
	require.NotContains(t, string(body), "gpt-other")
	account.Credentials["model_mapping"] = map[string]any{"public-new": "gpt-7-new", "native-alias": "gpt-native"}
	projected, err := projectAccountModelsBody(body, account, &Group{ID: 1, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"public-new"}}}, false)
	require.NoError(t, err)
	require.Contains(t, string(projected), "public-new")
	// Group listing policy is enforced at the existing group merge boundary.
	require.Contains(t, string(projected), "reasoning_efforts")
}

// excelBPSAccessOnlyUpstream serves the BPS access catalog. Native OAuth
// manifests do not go through httpUpstream, so any other path is a test bug.
func excelBPSAccessOnlyUpstream(t *testing.T, calls *atomic.Int32) HTTPUpstream {
	return &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		require.True(t, strings.HasPrefix(req.URL.Path, "/basispoints/"), "unexpected upstream path %s", req.URL.Path)
		calls.Add(1)
		return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
	}}
}

// useNativeCodexManifestServer points the ChatGPT Codex manifest endpoint at a
// local server so native OAuth discovery never leaves the test process.
func useNativeCodexManifestServer(t *testing.T, calls *atomic.Int32) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-native","visibility":"list"}]}`))
	}))
	t.Cleanup(server.Close)
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original })
}
