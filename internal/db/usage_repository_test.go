package db

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

func TestUsageRepositoryCreate(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create an account first (needed for foreign key)
	accountRepo := NewAccountRepository(database)
	account := &models.Account{
		Provider:    models.ProviderAnthropic,
		ProfileName: "test-account",
	}
	if err := accountRepo.Create(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}

	repo := NewUsageRepository(database)

	record := &models.UsageRecord{
		AccountID:    account.ID,
		Provider:     models.ProviderAnthropic,
		Model:        "claude-3-opus",
		InputTokens:  1000,
		OutputTokens: 500,
		CostCents:    15,
	}

	if err := repo.Create(ctx, record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if record.ID == "" {
		t.Error("expected ID to be set")
	}
	if record.TotalTokens != 1500 {
		t.Errorf("expected TotalTokens 1500, got %d", record.TotalTokens)
	}
	if record.RequestCount != 1 {
		t.Errorf("expected RequestCount 1, got %d", record.RequestCount)
	}

	// Retrieve and verify
	retrieved, err := repo.Get(ctx, record.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if retrieved.AccountID != account.ID {
		t.Errorf("expected AccountID %s, got %s", account.ID, retrieved.AccountID)
	}
	if retrieved.Model != "claude-3-opus" {
		t.Errorf("expected Model 'claude-3-opus', got %s", retrieved.Model)
	}
	if retrieved.InputTokens != 1000 {
		t.Errorf("expected InputTokens 1000, got %d", retrieved.InputTokens)
	}
}

func TestUsageRepositoryQuery(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account1 := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "account1"}
	account2 := &models.Account{Provider: models.ProviderOpenAI, ProfileName: "account2"}
	if err := accountRepo.Create(ctx, account1); err != nil {
		t.Fatalf("create account1: %v", err)
	}
	if err := accountRepo.Create(ctx, account2); err != nil {
		t.Fatalf("create account2: %v", err)
	}

	repo := NewUsageRepository(database)

	now := time.Now().UTC()

	// Create usage records for different accounts
	records := []*models.UsageRecord{
		{AccountID: account1.ID, Provider: models.ProviderAnthropic, InputTokens: 100, RecordedAt: now.Add(-2 * time.Hour)},
		{AccountID: account1.ID, Provider: models.ProviderAnthropic, InputTokens: 200, RecordedAt: now.Add(-1 * time.Hour)},
		{AccountID: account2.ID, Provider: models.ProviderOpenAI, InputTokens: 300, RecordedAt: now},
	}

	for _, r := range records {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Query by account
	results, err := repo.Query(ctx, models.UsageQuery{AccountID: &account1.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for account1, got %d", len(results))
	}

	// Query by provider
	anthropic := models.ProviderAnthropic
	results, err = repo.Query(ctx, models.UsageQuery{Provider: &anthropic})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for anthropic, got %d", len(results))
	}

	// Query with time filter
	since := now.Add(-90 * time.Minute)
	results, err = repo.Query(ctx, models.UsageQuery{Since: &since})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results since 90 min ago, got %d", len(results))
	}
}

func TestUsageRepositorySummarizeByAccount(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "test"}
	if err := accountRepo.Create(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}

	repo := NewUsageRepository(database)

	// Create multiple usage records
	for i := 0; i < 5; i++ {
		record := &models.UsageRecord{
			AccountID:    account.ID,
			Provider:     models.ProviderAnthropic,
			InputTokens:  100,
			OutputTokens: 50,
			CostCents:    5,
			RequestCount: 1,
		}
		if err := repo.Create(ctx, record); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	summary, err := repo.SummarizeByAccount(ctx, account.ID, nil, nil)
	if err != nil {
		t.Fatalf("SummarizeByAccount: %v", err)
	}

	if summary.InputTokens != 500 {
		t.Errorf("expected InputTokens 500, got %d", summary.InputTokens)
	}
	if summary.OutputTokens != 250 {
		t.Errorf("expected OutputTokens 250, got %d", summary.OutputTokens)
	}
	if summary.TotalTokens != 750 {
		t.Errorf("expected TotalTokens 750, got %d", summary.TotalTokens)
	}
	if summary.TotalCostCents != 25 {
		t.Errorf("expected TotalCostCents 25, got %d", summary.TotalCostCents)
	}
	if summary.RequestCount != 5 {
		t.Errorf("expected RequestCount 5, got %d", summary.RequestCount)
	}
	if summary.RecordCount != 5 {
		t.Errorf("expected RecordCount 5, got %d", summary.RecordCount)
	}
}

func TestUsageRepositorySummarizeAll(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account1 := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "account1"}
	account2 := &models.Account{Provider: models.ProviderOpenAI, ProfileName: "account2"}
	if err := accountRepo.Create(ctx, account1); err != nil {
		t.Fatalf("create account1: %v", err)
	}
	if err := accountRepo.Create(ctx, account2); err != nil {
		t.Fatalf("create account2: %v", err)
	}

	repo := NewUsageRepository(database)

	records := []*models.UsageRecord{
		{AccountID: account1.ID, Provider: models.ProviderAnthropic, InputTokens: 100, CostCents: 10},
		{AccountID: account2.ID, Provider: models.ProviderOpenAI, InputTokens: 200, CostCents: 20},
	}
	for _, r := range records {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	summary, err := repo.SummarizeAll(ctx, nil, nil)
	if err != nil {
		t.Fatalf("SummarizeAll: %v", err)
	}

	if summary.InputTokens != 300 {
		t.Errorf("expected InputTokens 300, got %d", summary.InputTokens)
	}
	if summary.TotalCostCents != 30 {
		t.Errorf("expected TotalCostCents 30, got %d", summary.TotalCostCents)
	}
	if summary.RecordCount != 2 {
		t.Errorf("expected RecordCount 2, got %d", summary.RecordCount)
	}
}

func TestUsageRepositoryGetDailyUsage(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "test"}
	if err := accountRepo.Create(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}

	repo := NewUsageRepository(database)

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	// Create usage for today and yesterday
	records := []*models.UsageRecord{
		{AccountID: account.ID, Provider: models.ProviderAnthropic, InputTokens: 100, RecordedAt: now},
		{AccountID: account.ID, Provider: models.ProviderAnthropic, InputTokens: 200, RecordedAt: now},
		{AccountID: account.ID, Provider: models.ProviderAnthropic, InputTokens: 300, RecordedAt: yesterday},
	}
	for _, r := range records {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	since := yesterday.Add(-1 * time.Hour)
	until := now.Add(1 * time.Hour)
	daily, err := repo.GetDailyUsage(ctx, account.ID, since, until, 30)
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}

	if len(daily) != 2 {
		t.Errorf("expected 2 daily records, got %d", len(daily))
	}

	// Check that today's usage is aggregated correctly
	var todayTokens int64
	for _, d := range daily {
		if d.Date == now.Format("2006-01-02") {
			todayTokens = d.InputTokens
		}
	}
	if todayTokens != 300 {
		t.Errorf("expected today's tokens 300, got %d", todayTokens)
	}
}

func TestUsageRepositoryGetTopAccountsByUsage(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	accounts := make([]*models.Account, 3)
	for i := 0; i < 3; i++ {
		accounts[i] = &models.Account{
			Provider:    models.ProviderAnthropic,
			ProfileName: string(rune('a' + i)),
		}
		if err := accountRepo.Create(ctx, accounts[i]); err != nil {
			t.Fatalf("create account: %v", err)
		}
	}

	repo := NewUsageRepository(database)

	// Create different amounts of usage for each account
	usages := []int64{1000, 500, 2000} // account c should be first
	for i, usage := range usages {
		record := &models.UsageRecord{
			AccountID:   accounts[i].ID,
			Provider:    models.ProviderAnthropic,
			TotalTokens: usage,
		}
		if err := repo.Create(ctx, record); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	top, err := repo.GetTopAccountsByUsage(ctx, nil, nil, 10)
	if err != nil {
		t.Fatalf("GetTopAccountsByUsage: %v", err)
	}

	if len(top) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(top))
	}

	// First should be account with 2000 tokens
	if top[0].TotalTokens != 2000 {
		t.Errorf("expected top account to have 2000 tokens, got %d", top[0].TotalTokens)
	}
	// Last should be account with 500 tokens
	if top[2].TotalTokens != 500 {
		t.Errorf("expected last account to have 500 tokens, got %d", top[2].TotalTokens)
	}
}

func TestUsageRepositoryDelete(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "test"}
	if err := accountRepo.Create(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}

	repo := NewUsageRepository(database)

	record := &models.UsageRecord{
		AccountID: account.ID,
		Provider:  models.ProviderAnthropic,
	}
	if err := repo.Create(ctx, record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(ctx, record.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.Get(ctx, record.ID)
	if err != ErrUsageRecordNotFound {
		t.Errorf("expected ErrUsageRecordNotFound, got %v", err)
	}
}

func TestUsageRepositoryDeleteOlderThan(t *testing.T) {
	ctx := context.Background()

	database, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	accountRepo := NewAccountRepository(database)
	account := &models.Account{Provider: models.ProviderAnthropic, ProfileName: "test"}
	if err := accountRepo.Create(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}

	repo := NewUsageRepository(database)

	now := time.Now().UTC()

	// Create old and new records
	records := []*models.UsageRecord{
		{AccountID: account.ID, Provider: models.ProviderAnthropic, RecordedAt: now.Add(-48 * time.Hour)},
		{AccountID: account.ID, Provider: models.ProviderAnthropic, RecordedAt: now.Add(-24 * time.Hour)},
		{AccountID: account.ID, Provider: models.ProviderAnthropic, RecordedAt: now},
	}
	for _, r := range records {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Delete records older than 36 hours
	cutoff := now.Add(-36 * time.Hour)
	deleted, err := repo.DeleteOlderThan(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}

	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify remaining records
	results, err := repo.Query(ctx, models.UsageQuery{AccountID: &account.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 remaining, got %d", len(results))
	}
}

func TestUsageRecordValidation(t *testing.T) {
	tests := []struct {
		name    string
		record  *models.UsageRecord
		wantErr bool
	}{
		{
			name: "valid",
			record: &models.UsageRecord{
				AccountID:   "account-1",
				Provider:    models.ProviderAnthropic,
				TotalTokens: 100,
			},
			wantErr: false,
		},
		{
			name: "missing account_id",
			record: &models.UsageRecord{
				Provider:    models.ProviderAnthropic,
				TotalTokens: 100,
			},
			wantErr: true,
		},
		{
			name: "missing provider",
			record: &models.UsageRecord{
				AccountID:   "account-1",
				TotalTokens: 100,
			},
			wantErr: true,
		},
		{
			name: "negative tokens",
			record: &models.UsageRecord{
				AccountID:   "account-1",
				Provider:    models.ProviderAnthropic,
				TotalTokens: -100,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.record.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}
