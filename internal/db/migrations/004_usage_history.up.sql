-- Migration: 004_usage_history
-- Description: Add usage_records table for tracking token usage and costs
-- Created: 2025-12-22

-- ============================================================================
-- USAGE_RECORDS TABLE
-- ============================================================================
-- Stores individual usage records for token tracking and cost estimation.
-- Records can be aggregated by account, day, session, etc.

CREATE TABLE IF NOT EXISTS usage_records (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    session_id TEXT,
    provider TEXT NOT NULL CHECK (provider IN ('anthropic', 'openai', 'google', 'custom')),
    model TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cost_cents INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 1,
    recorded_at TEXT NOT NULL DEFAULT (datetime('now')),
    metadata_json TEXT
);

-- Index for querying by account
CREATE INDEX IF NOT EXISTS idx_usage_records_account_id ON usage_records(account_id);

-- Index for querying by agent
CREATE INDEX IF NOT EXISTS idx_usage_records_agent_id ON usage_records(agent_id);

-- Index for querying by session
CREATE INDEX IF NOT EXISTS idx_usage_records_session_id ON usage_records(session_id);

-- Index for querying by provider
CREATE INDEX IF NOT EXISTS idx_usage_records_provider ON usage_records(provider);

-- Index for time-based queries
CREATE INDEX IF NOT EXISTS idx_usage_records_recorded_at ON usage_records(recorded_at);

-- Composite index for daily aggregation by account
CREATE INDEX IF NOT EXISTS idx_usage_records_account_day ON usage_records(account_id, date(recorded_at));

-- Composite index for provider + time queries
CREATE INDEX IF NOT EXISTS idx_usage_records_provider_time ON usage_records(provider, recorded_at);

-- ============================================================================
-- DAILY_USAGE_CACHE TABLE (optional materialized view)
-- ============================================================================
-- Pre-aggregated daily usage for faster queries.
-- Updated by triggers or periodic background jobs.

CREATE TABLE IF NOT EXISTS daily_usage_cache (
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date TEXT NOT NULL,  -- YYYY-MM-DD
    provider TEXT NOT NULL CHECK (provider IN ('anthropic', 'openai', 'google', 'custom')),
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cost_cents INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 0,
    record_count INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (account_id, date, provider)
);

-- Index for date range queries
CREATE INDEX IF NOT EXISTS idx_daily_usage_cache_date ON daily_usage_cache(date);

-- Index for provider queries
CREATE INDEX IF NOT EXISTS idx_daily_usage_cache_provider ON daily_usage_cache(provider);
