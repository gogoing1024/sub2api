package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

const bpsListAccessFixture = `{"allowed":true,"model_catalog":{"models":[{"id":"gpt-5.6-sol"},{"id":"gpt-6-astra"},{"id":"gpt-7-new"}]}}`

func excelBPSListTestGateway(status int, body string) (*OpenAIGatewayService, *int) {
	calls := 0
	gateway := openAIClientToolsTestService(nil)
	gateway.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		if req.URL.Path != "/basispoints/api/responses/access" {
			return nil, errors.New("unexpected request " + req.URL.String())
		}
		response := ordinaryModelsUpstreamResponse(body)
		response.StatusCode = status
		return response, nil
	}}
	return gateway, &calls
}

func excelBPSListModelIDs(models []openai.Model) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// Live BPS discovery within the whitelist, projected through the mapping.
func TestListExcelBPSAccountModelsUsesDiscovery(t *testing.T) {
	gateway, calls := excelBPSListTestGateway(http.StatusOK, bpsListAccessFixture)
	account := excelAccount()
	account.Extra = map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-5.6-sol", "gpt-7-new"}}
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-7-new"}, excelBPSListModelIDs(gateway.ListExcelBPSAccountModels(context.Background(), account)))

	mapped := excelAccount()
	mapped.ID = 301
	mapped.Extra = map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-5.6-sol", "gpt-7-new"}}
	mapped.Credentials["model_mapping"] = map[string]any{"my-sol": "gpt-5.6-sol", "gpt-7-new": "gpt-7-new", "astra": "gpt-6-astra"}
	models := gateway.ListExcelBPSAccountModels(context.Background(), mapped)
	require.ElementsMatch(t, []string{"my-sol", "gpt-7-new"}, excelBPSListModelIDs(models), "mapping keys outside the whitelist stay out")
	for _, model := range models {
		require.NotEmpty(t, model.DisplayName)
	}
	require.Equal(t, 2, *calls)
}

// When discovery fails only the configured BPS models are listed, never the
// native OpenAI defaults, and the order is stable with the whitelist first.
func TestListExcelBPSAccountModelsFallsBackToConfiguration(t *testing.T) {
	gateway, _ := excelBPSListTestGateway(http.StatusInternalServerError, `{}`)
	whitelist := []any{"gpt-5.6-terra", "gpt-5.6-sol"}

	mapped := excelAccount()
	mapped.Extra = map[string]any{ExcelBPSModelsExtraKey: whitelist}
	mapped.Credentials["model_mapping"] = map[string]any{"z-sol": "gpt-5.6-sol", "a-terra": "gpt-5.6-terra", "out": "gpt-9", "x-*": "gpt-5.6-sol"}
	for i := 0; i < 5; i++ {
		require.Equal(t, []string{"a-terra", "z-sol"}, excelBPSListModelIDs(gateway.ListExcelBPSAccountModels(context.Background(), mapped)))
	}

	unmapped := excelAccount()
	unmapped.ID = 302
	unmapped.Extra = map[string]any{ExcelBPSModelsExtraKey: whitelist}
	require.Equal(t, []string{"gpt-5.6-terra", "gpt-5.6-sol"}, excelBPSListModelIDs(gateway.ListExcelBPSAccountModels(context.Background(), unmapped)))

	all := excelAccount()
	all.ID = 303
	all.Extra = nil
	require.Empty(t, gateway.ListExcelBPSAccountModels(context.Background(), all))
}

func TestListExcelBPSAccountModelsIgnoresOtherAccounts(t *testing.T) {
	gateway, calls := excelBPSListTestGateway(http.StatusOK, bpsListAccessFixture)
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		account := &Account{ID: 9, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}}}
		require.Nil(t, gateway.ListExcelBPSAccountModels(context.Background(), account))
	}
	require.Zero(t, *calls)
}

type countingExcelBPSModelSource struct {
	calls  atomic.Int32
	models map[int64][]string
}

func (s *countingExcelBPSModelSource) ExcelBPSAccountModelIDs(_ context.Context, account *Account) []string {
	s.calls.Add(1)
	return s.models[account.ID]
}

func excelBPSListingService(source ExcelBPSModelSource, accounts ...Account) (*GatewayService, *int64) {
	groupID := int64(77)
	svc := &GatewayService{
		accountRepo:        &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: accounts}},
		modelsListCache:    gocache.New(time.Minute, time.Minute),
		modelsListCacheTTL: time.Minute,
	}
	if source != nil {
		svc.SetExcelBPSModelSource(source)
	}
	return svc, &groupID
}

// BPS-only accounts contribute their BPS models to /v1/models, not the native
// defaults or mapping keys outside their whitelist.
func TestGetAvailableModelsListsExcelBPSModels(t *testing.T) {
	bps := *excelAccount()
	bps.Extra = map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-5.6-sol"}}
	bps.Credentials["model_mapping"] = map[string]any{"my-sol": "gpt-5.6-sol", "astra": "gpt-6-astra"}

	source := &countingExcelBPSModelSource{models: map[int64][]string{bps.ID: {"my-sol"}}}
	svc, groupID := excelBPSListingService(source, bps)
	require.Equal(t, []string{"my-sol"}, svc.GetAvailableModels(context.Background(), groupID, PlatformOpenAI))
	require.EqualValues(t, 1, source.calls.Load())

	// Without discovery, the configuration alone: still no native defaults.
	unmapped := *excelAccount()
	unmapped.Extra = map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-6-astra", "gpt-5.6-sol"}}
	svc, groupID = excelBPSListingService(nil, unmapped)
	got := svc.GetAvailableModels(context.Background(), groupID, PlatformOpenAI)
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, got)
	for _, model := range got {
		require.NotContains(t, model, "gpt-image")
		require.NotEqual(t, "codex-auto-review", model)
	}
}

// In a mixed pool the native accounts contribute exactly what they would as a
// pool of their own; a pure native pool never consults the BPS source.
func TestGetAvailableModelsKeepsNativeContribution(t *testing.T) {
	nativeMapped := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"configured-model": "gpt-5"}}}
	nativeUnmapped := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	bps := *excelAccount()
	for name, natives := range map[string][]Account{
		"mapped":   {nativeMapped},
		"unmapped": {nativeUnmapped},
		"both":     {nativeMapped, nativeUnmapped},
	} {
		t.Run(name, func(t *testing.T) {
			source := &countingExcelBPSModelSource{models: map[int64][]string{bps.ID: {"gpt-bps-only"}}}
			pureSvc, pureGroup := excelBPSListingService(source, natives...)
			pure := pureSvc.GetAvailableModels(context.Background(), pureGroup, PlatformOpenAI)
			require.Zero(t, source.calls.Load(), "a pool without BPS-only accounts never asks the BPS source")
			if pure == nil {
				pure = filterRetiredOpenAIModelIDs(natives, openai.DefaultModelIDs())
			}

			mixedSvc, mixedGroup := excelBPSListingService(source, append(append([]Account{}, natives...), bps)...)
			mixed := mixedSvc.GetAvailableModels(context.Background(), mixedGroup, PlatformOpenAI)
			require.Equal(t, dedupeAndSortModelIDs(append(append([]string{}, pure...), "gpt-bps-only")), mixed)
		})
	}
}
