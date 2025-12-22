// Package db provides SQLite database access for Swarm.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/swarm/internal/models"
)

// Usage repository errors.
var (
	ErrUsageRecordNotFound = errors.New("usage record not found")
	ErrInvalidUsageRecord  = errors.New("invalid usage record")
)

// UsageRepository handles usage record persistence.
type UsageRepository struct {
	db *DB
}

// NewUsageRepository creates a new UsageRepository.
func NewUsageRepository(db *DB) *UsageRepository {
	return &UsageRepository{db: db}
}

// Create inserts a new usage record.
func (r *UsageRepository) Create(ctx context.Context, record *models.UsageRecord) error {
	if record.AccountID == "" || record.Provider == "" {
		return ErrInvalidUsageRecord
	}

	if record.ID == "" {
		record.ID = uuid.New().String()
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = time.Now().UTC()
	}
	if record.TotalTokens == 0 {
		record.TotalTokens = record.InputTokens + record.OutputTokens
	}
	if record.RequestCount == 0 {
		record.RequestCount = 1
	}

	var metadataJSON *string
	if record.Metadata != nil {
		data, err := json.Marshal(record.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
		s := string(data)
		metadataJSON = &s
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			id, account_id, agent_id, session_id, provider, model,
			input_tokens, output_tokens, total_tokens, cost_cents,
			request_count, recorded_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		record.AccountID,
		nullString(record.AgentID),
		nullString(record.SessionID),
		string(record.Provider),
		nullString(record.Model),
		record.InputTokens,
		record.OutputTokens,
		record.TotalTokens,
		record.CostCents,
		record.RequestCount,
		record.RecordedAt.UTC().Format(time.RFC3339),
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to insert usage record: %w", err)
	}

	return nil
}

// Get retrieves a usage record by ID.
func (r *UsageRepository) Get(ctx context.Context, id string) (*models.UsageRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, agent_id, session_id, provider, model,
			input_tokens, output_tokens, total_tokens, cost_cents,
			request_count, recorded_at, metadata_json
		FROM usage_records WHERE id = ?
	`, id)

	return r.scanUsageRecord(row)
}

// Query retrieves usage records matching the given filters.
func (r *UsageRepository) Query(ctx context.Context, q models.UsageQuery) ([]*models.UsageRecord, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, account_id, agent_id, session_id, provider, model,
		input_tokens, output_tokens, total_tokens, cost_cents,
		request_count, recorded_at, metadata_json
		FROM usage_records WHERE 1=1`
	args := []any{}

	if q.AccountID != nil {
		query += ` AND account_id = ?`
		args = append(args, *q.AccountID)
	}
	if q.AgentID != nil {
		query += ` AND agent_id = ?`
		args = append(args, *q.AgentID)
	}
	if q.SessionID != nil {
		query += ` AND session_id = ?`
		args = append(args, *q.SessionID)
	}
	if q.Provider != nil {
		query += ` AND provider = ?`
		args = append(args, string(*q.Provider))
	}
	if q.Model != nil {
		query += ` AND model = ?`
		args = append(args, *q.Model)
	}
	if q.Since != nil {
		query += ` AND recorded_at >= ?`
		args = append(args, q.Since.UTC().Format(time.RFC3339))
	}
	if q.Until != nil {
		query += ` AND recorded_at < ?`
		args = append(args, q.Until.UTC().Format(time.RFC3339))
	}

	query += ` ORDER BY recorded_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage records: %w", err)
	}
	defer rows.Close()

	var records []*models.UsageRecord
	for rows.Next() {
		record, err := r.scanUsageRecordFromRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage records: %w", err)
	}

	return records, nil
}

// Delete removes a usage record by ID.
func (r *UsageRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM usage_records WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete usage record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if affected == 0 {
		return ErrUsageRecordNotFound
	}
	return nil
}

// SummarizeByAccount returns aggregated usage for an account.
func (r *UsageRepository) SummarizeByAccount(ctx context.Context, accountID string, since, until *time.Time) (*models.UsageSummary, error) {
	query := `SELECT 
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost_cents), 0) as cost_cents,
		COALESCE(SUM(request_count), 0) as request_count,
		COUNT(*) as record_count
		FROM usage_records WHERE account_id = ?`
	args := []any{accountID}

	if since != nil {
		query += ` AND recorded_at >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	if until != nil {
		query += ` AND recorded_at < ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}

	var summary models.UsageSummary
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.InputTokens,
		&summary.OutputTokens,
		&summary.TotalTokens,
		&summary.TotalCostCents,
		&summary.RequestCount,
		&summary.RecordCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize usage: %w", err)
	}

	summary.AccountID = accountID
	summary.Period = "custom"
	if since != nil {
		summary.PeriodStart = *since
	}
	if until != nil {
		summary.PeriodEnd = *until
	}

	return &summary, nil
}

// SummarizeByProvider returns aggregated usage for a provider.
func (r *UsageRepository) SummarizeByProvider(ctx context.Context, provider models.Provider, since, until *time.Time) (*models.UsageSummary, error) {
	query := `SELECT 
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost_cents), 0) as cost_cents,
		COALESCE(SUM(request_count), 0) as request_count,
		COUNT(*) as record_count
		FROM usage_records WHERE provider = ?`
	args := []any{string(provider)}

	if since != nil {
		query += ` AND recorded_at >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	if until != nil {
		query += ` AND recorded_at < ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}

	var summary models.UsageSummary
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.InputTokens,
		&summary.OutputTokens,
		&summary.TotalTokens,
		&summary.TotalCostCents,
		&summary.RequestCount,
		&summary.RecordCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize usage: %w", err)
	}

	summary.Provider = provider
	summary.Period = "custom"
	if since != nil {
		summary.PeriodStart = *since
	}
	if until != nil {
		summary.PeriodEnd = *until
	}

	return &summary, nil
}

// SummarizeAll returns aggregated usage for all accounts.
func (r *UsageRepository) SummarizeAll(ctx context.Context, since, until *time.Time) (*models.UsageSummary, error) {
	query := `SELECT 
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost_cents), 0) as cost_cents,
		COALESCE(SUM(request_count), 0) as request_count,
		COUNT(*) as record_count
		FROM usage_records WHERE 1=1`
	args := []any{}

	if since != nil {
		query += ` AND recorded_at >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	if until != nil {
		query += ` AND recorded_at < ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}

	var summary models.UsageSummary
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.InputTokens,
		&summary.OutputTokens,
		&summary.TotalTokens,
		&summary.TotalCostCents,
		&summary.RequestCount,
		&summary.RecordCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize usage: %w", err)
	}

	summary.Period = "all"
	if since != nil {
		summary.PeriodStart = *since
	}
	if until != nil {
		summary.PeriodEnd = *until
	}

	return &summary, nil
}

// GetDailyUsage returns usage aggregated by day for an account.
func (r *UsageRepository) GetDailyUsage(ctx context.Context, accountID string, since, until time.Time, limit int) ([]*models.DailyUsage, error) {
	if limit <= 0 {
		limit = 30
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			date(recorded_at) as date,
			provider,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cost_cents), 0) as cost_cents,
			COALESCE(SUM(request_count), 0) as request_count
		FROM usage_records 
		WHERE account_id = ? AND recorded_at >= ? AND recorded_at < ?
		GROUP BY date(recorded_at), provider
		ORDER BY date DESC
		LIMIT ?
	`, accountID, since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get daily usage: %w", err)
	}
	defer rows.Close()

	var dailyUsage []*models.DailyUsage
	for rows.Next() {
		var du models.DailyUsage
		var provider string
		if err := rows.Scan(
			&du.Date,
			&provider,
			&du.InputTokens,
			&du.OutputTokens,
			&du.TotalTokens,
			&du.CostCents,
			&du.RequestCount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan daily usage: %w", err)
		}
		du.AccountID = accountID
		du.Provider = models.Provider(provider)
		dailyUsage = append(dailyUsage, &du)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating daily usage: %w", err)
	}

	return dailyUsage, nil
}

// GetTopAccountsByUsage returns accounts sorted by total token usage.
func (r *UsageRepository) GetTopAccountsByUsage(ctx context.Context, since, until *time.Time, limit int) ([]*models.UsageSummary, error) {
	if limit <= 0 {
		limit = 10
	}

	query := `SELECT 
		account_id,
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(cost_cents), 0) as cost_cents,
		COALESCE(SUM(request_count), 0) as request_count,
		COUNT(*) as record_count
		FROM usage_records WHERE 1=1`
	args := []any{}

	if since != nil {
		query += ` AND recorded_at >= ?`
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	if until != nil {
		query += ` AND recorded_at < ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}

	query += ` GROUP BY account_id ORDER BY total_tokens DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get top accounts: %w", err)
	}
	defer rows.Close()

	var summaries []*models.UsageSummary
	for rows.Next() {
		var summary models.UsageSummary
		if err := rows.Scan(
			&summary.AccountID,
			&summary.InputTokens,
			&summary.OutputTokens,
			&summary.TotalTokens,
			&summary.TotalCostCents,
			&summary.RequestCount,
			&summary.RecordCount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan usage summary: %w", err)
		}
		summary.Period = "custom"
		if since != nil {
			summary.PeriodStart = *since
		}
		if until != nil {
			summary.PeriodEnd = *until
		}
		summaries = append(summaries, &summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage summaries: %w", err)
	}

	return summaries, nil
}

// UpdateDailyCache updates the daily usage cache for an account.
func (r *UsageRepository) UpdateDailyCache(ctx context.Context, accountID, date string, provider models.Provider) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO daily_usage_cache (
			account_id, date, provider,
			input_tokens, output_tokens, total_tokens,
			cost_cents, request_count, record_count, updated_at
		)
		SELECT 
			account_id,
			date(recorded_at) as date,
			provider,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cost_cents), 0),
			COALESCE(SUM(request_count), 0),
			COUNT(*),
			datetime('now')
		FROM usage_records
		WHERE account_id = ? AND date(recorded_at) = ? AND provider = ?
		GROUP BY account_id, date(recorded_at), provider
	`, accountID, date, string(provider))
	if err != nil {
		return fmt.Errorf("failed to update daily cache: %w", err)
	}
	return nil
}

// DeleteOlderThan removes usage records older than the given time.
func (r *UsageRepository) DeleteOlderThan(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}

	result, err := r.db.ExecContext(ctx, `
		DELETE FROM usage_records WHERE id IN (
			SELECT id FROM usage_records WHERE recorded_at < ? ORDER BY recorded_at LIMIT ?
		)
	`, before.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old usage records: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get deleted count: %w", err)
	}
	return count, nil
}

func (r *UsageRepository) scanUsageRecord(row *sql.Row) (*models.UsageRecord, error) {
	var record models.UsageRecord
	var agentID, sessionID, model, metadataJSON sql.NullString
	var provider, recordedAt string

	err := row.Scan(
		&record.ID,
		&record.AccountID,
		&agentID,
		&sessionID,
		&provider,
		&model,
		&record.InputTokens,
		&record.OutputTokens,
		&record.TotalTokens,
		&record.CostCents,
		&record.RequestCount,
		&recordedAt,
		&metadataJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUsageRecordNotFound
		}
		return nil, fmt.Errorf("failed to scan usage record: %w", err)
	}

	record.Provider = models.Provider(provider)
	if agentID.Valid {
		record.AgentID = agentID.String
	}
	if sessionID.Valid {
		record.SessionID = sessionID.String
	}
	if model.Valid {
		record.Model = model.String
	}
	if t, err := time.Parse(time.RFC3339, recordedAt); err == nil {
		record.RecordedAt = t
	}
	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &record.Metadata); err != nil {
			r.db.logger.Warn().Err(err).Str("usage_id", record.ID).Msg("failed to parse usage metadata")
		}
	}

	return &record, nil
}

func (r *UsageRepository) scanUsageRecordFromRows(rows *sql.Rows) (*models.UsageRecord, error) {
	var record models.UsageRecord
	var agentID, sessionID, model, metadataJSON sql.NullString
	var provider, recordedAt string

	if err := rows.Scan(
		&record.ID,
		&record.AccountID,
		&agentID,
		&sessionID,
		&provider,
		&model,
		&record.InputTokens,
		&record.OutputTokens,
		&record.TotalTokens,
		&record.CostCents,
		&record.RequestCount,
		&recordedAt,
		&metadataJSON,
	); err != nil {
		return nil, fmt.Errorf("failed to scan usage record: %w", err)
	}

	record.Provider = models.Provider(provider)
	if agentID.Valid {
		record.AgentID = agentID.String
	}
	if sessionID.Valid {
		record.SessionID = sessionID.String
	}
	if model.Valid {
		record.Model = model.String
	}
	if t, err := time.Parse(time.RFC3339, recordedAt); err == nil {
		record.RecordedAt = t
	}
	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &record.Metadata); err != nil {
			r.db.logger.Warn().Err(err).Str("usage_id", record.ID).Msg("failed to parse usage metadata")
		}
	}

	return &record, nil
}

func nullString(s string) sql.NullString {
	if strings.TrimSpace(s) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
