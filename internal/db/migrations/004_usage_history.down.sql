-- Migration: 004_usage_history (rollback)
-- Description: Remove usage_records and daily_usage_cache tables

DROP TABLE IF EXISTS daily_usage_cache;
DROP TABLE IF EXISTS usage_records;
