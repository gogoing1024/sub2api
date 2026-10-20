package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// ListExcelBPSAccountModels lists the public model names a BPS-only account
// serves, for its test picker and for model listings. It prefers live BPS
// discovery (already narrowed to the account's BPS whitelist, then projected
// through its model mapping) and falls back to the account configuration when
// discovery fails or is empty. It never falls back to the native OpenAI
// defaults, which BPS cannot serve. Other account types get nil.
func (s *OpenAIGatewayService) ListExcelBPSAccountModels(ctx context.Context, account *Account) []openai.Model {
	if !account.IsBPSOAuth() {
		return nil
	}
	if s != nil {
		if response, err := s.fetchExcelBPSAccountModels(ctx, account); err == nil {
			if models := excelBPSProjectedModels(response.Body, account); len(models) > 0 {
				return models
			}
		}
	}
	return ExcelBPSConfiguredModels(account)
}

// ExcelBPSAccountModelIDs is ListExcelBPSAccountModels as model IDs; it lets
// GatewayService model listings use BPS discovery for BPS-only accounts.
func (s *OpenAIGatewayService) ExcelBPSAccountModelIDs(ctx context.Context, account *Account) []string {
	models := s.ListExcelBPSAccountModels(ctx, account)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func excelBPSProjectedModels(body []byte, account *Account) []openai.Model {
	projected, err := projectAccountModelsBody(body, account, nil, false)
	if err != nil {
		return nil
	}
	var payload struct {
		Data []openai.Model `json:"data"`
	}
	if json.Unmarshal(projected, &payload) != nil {
		return nil
	}
	models := make([]openai.Model, 0, len(payload.Data))
	for _, model := range payload.Data {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		fillExcelBPSModelFields(&model)
		models = append(models, model)
	}
	return models
}

func fillExcelBPSModelFields(model *openai.Model) {
	if strings.TrimSpace(model.DisplayName) == "" {
		model.DisplayName = openaiCodexDisplayName(model.ID)
	}
	if strings.TrimSpace(model.Type) == "" {
		model.Type = "model"
	}
	if strings.TrimSpace(model.Object) == "" {
		model.Object = "model"
	}
}

// ExcelBPSConfiguredModels lists a BPS-only account's models from its
// configuration alone: the mapping keys whose target is on the BPS whitelist
// (ordered by whitelist position, then name, so the first entry is stable), or
// the whitelist itself without a mapping. An account allowing every BPS model
// with no mapping has nothing to list without discovery.
func ExcelBPSConfiguredModels(account *Account) []openai.Model {
	models := []openai.Model{}
	if !account.IsBPSOAuth() {
		return models
	}
	whitelist, scoped := excelBPSWhitelist(account)
	position := make(map[string]int, len(whitelist))
	for i, model := range whitelist {
		if _, seen := position[model]; !seen {
			position[model] = i
		}
	}
	var ids []string
	if mapping := account.GetModelMapping(); len(mapping) > 0 {
		for key := range mapping {
			key = strings.TrimSpace(key)
			if key == "" || strings.Contains(key, "*") || !account.IsExcelBPSEnabledForModel(key) {
				continue
			}
			ids = append(ids, key)
		}
		sort.Slice(ids, func(i, j int) bool {
			pi, iok := position[account.GetMappedModel(ids[i])]
			pj, jok := position[account.GetMappedModel(ids[j])]
			if iok != jok {
				return iok
			}
			if iok && pi != pj {
				return pi < pj
			}
			return ids[i] < ids[j]
		})
	} else if scoped {
		ids = whitelist
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		model := openai.Model{ID: id}
		fillExcelBPSModelFields(&model)
		models = append(models, model)
	}
	return models
}

// excelBPSWhitelist returns the stored BPS whitelist in order, and whether the
// account has one (a missing key allows every BPS model).
func excelBPSWhitelist(account *Account) ([]string, bool) {
	raw, scoped := account.Extra[ExcelBPSModelsExtraKey]
	if !scoped {
		return nil, false
	}
	var models []string
	switch value := raw.(type) {
	case []string:
		models = value
	case []any:
		for _, item := range value {
			if name, ok := item.(string); ok {
				models = append(models, name)
			}
		}
	}
	trimmed := make([]string, 0, len(models))
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			trimmed = append(trimmed, model)
		}
	}
	return trimmed, true
}

// ExcelBPSModelSource supplies the models a BPS-only account serves to
// GatewayService listings. OpenAIGatewayService implements it.
type ExcelBPSModelSource interface {
	ExcelBPSAccountModelIDs(ctx context.Context, account *Account) []string
}

// excelBPSListingModels collects the BPS-only accounts' contribution to a model
// listing: each account's own models, fetched concurrently (each discovery is
// cached and deduplicated per account). Without a source, the configuration
// alone is used.
func excelBPSListingModels(ctx context.Context, source ExcelBPSModelSource, accounts []*Account) []string {
	results := make([][]string, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		if source == nil {
			for _, model := range ExcelBPSConfiguredModels(account) {
				results[i] = append(results[i], model.ID)
			}
			continue
		}
		wg.Add(1)
		go func(i int, account *Account) {
			defer wg.Done()
			results[i] = source.ExcelBPSAccountModelIDs(ctx, account)
		}(i, account)
	}
	wg.Wait()
	var models []string
	for _, ids := range results {
		models = append(models, ids...)
	}
	return models
}
