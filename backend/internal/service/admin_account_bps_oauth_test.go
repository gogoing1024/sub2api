//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestCreateAccount_RejectsEmptyExcelBPSModelsWhitelist(t *testing.T) {
	repo := &createAccountKiroCreditRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}
	input := func(extra map[string]any) *CreateAccountInput {
		return &CreateAccountInput{
			Name: "bps", Platform: PlatformOpenAI, Type: AccountTypeBPSOAuth, SkipDefaultGroupBind: true,
			Credentials: map[string]any{"access_token": "at", "client_id": openai.ExcelClientID},
			Extra:       extra,
		}
	}

	_, err := svc.CreateAccount(context.Background(), input(map[string]any{ExcelBPSModelsExtraKey: []any{}}))
	require.Error(t, err)
	require.Zero(t, repo.createCalls)

	_, err = svc.CreateAccount(context.Background(), input(nil))
	require.NoError(t, err, "no whitelist means every BPS model")
	_, err = svc.CreateAccount(context.Background(), input(map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-6-astra"}}))
	require.NoError(t, err)
	require.Equal(t, 2, repo.createCalls)
}

func TestUpdateAccount_BPSOAuthWhitelistAndType(t *testing.T) {
	accountID := int64(310)
	newRepo := func() *updateAccountOveragesRepoStub {
		return &updateAccountOveragesRepoStub{account: &Account{
			ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeBPSOAuth, Status: StatusActive,
			Credentials: map[string]any{"access_token": "at", "client_id": openai.ExcelClientID},
			Extra:       map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-6-astra"}},
		}}
	}

	repo := newRepo()
	_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{ExcelBPSModelsExtraKey: []any{}},
	})
	require.Error(t, err)
	require.Zero(t, repo.updateCalls)

	// Dropping client_id while switching type must not leave the Excel token
	// on a native account.
	repo = newRepo()
	_, err = (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "rt"},
	})
	require.Error(t, err)
	require.Zero(t, repo.updateCalls)

	repo = newRepo()
	_, err = (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{ExcelBPSModelsExtraKey: []any{"gpt-5.6-sol"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
}
