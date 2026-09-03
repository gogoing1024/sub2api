-- Migration: 232_kiro_cache_source_mode
-- Kiro 分组缓存用量来源模式：
--   emulation_only  = 完全采用本地缓存模拟值，忽略上游缓存字段（默认，保持既有行为）
--   upstream_first  = 优先采用上游 metadataEvent.tokenUsage 的真实缓存量，
--                     上游未下发该结构时退回本地模拟值兜底
--
-- 与已有的 kiro_cache_emulation_mode（uniform / independent，控制模拟值上报**比例**）
-- 正交：本字段决定「用谁的数」，那个决定「模拟值按多少比例上报」。
-- 与 kiro_cache_emulation_enabled 也正交：开关关闭时本字段无意义。
--
-- 背景：Kiro 上游的 TokenUsage smithy schema 含 uncachedInputTokens / outputTokens /
-- totalTokens / cacheReadInputTokens / cacheWriteInputTokens 等字段，确实会下发真实
-- 缓存用量，但并非每次响应都带，故保留模拟器作为兜底。

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS kiro_cache_source_mode VARCHAR(24) NOT NULL DEFAULT 'emulation_only';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'groups'
           AND c.conname = 'groups_kiro_cache_source_mode_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_kiro_cache_source_mode_check
            CHECK (kiro_cache_source_mode IN ('emulation_only', 'upstream_first'));
    END IF;
END $$;

COMMENT ON COLUMN groups.kiro_cache_source_mode IS
    'Kiro 缓存用量来源：emulation_only = 完全用模拟值（默认）；upstream_first = 优先用上游真实缓存量、模拟兜底';
