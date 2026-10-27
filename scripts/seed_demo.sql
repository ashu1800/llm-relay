-- 清空旧数据
DELETE FROM channel_models;
DELETE FROM channels;
DELETE FROM api_keys;
DELETE FROM request_logs;

INSERT INTO channels (id, name, protocol, base_url, currency, enabled, health_status, created_at, updated_at) VALUES
(1, 'Anthropic 官网直连', 'anthropic-messages', 'https://api.anthropic.com', 'USD', true, 'healthy', NOW() - INTERVAL '7 days', NOW()),
(2, 'DeepSeek 官方 API', 'openai-chat', 'https://api.deepseek.com', 'CNY', true, 'healthy', NOW() - INTERVAL '7 days', NOW()),
(3, 'Google AI Studio', 'gemini-generateContent', 'https://generativelanguage.googleapis.com', 'USD', true, 'healthy', NOW() - INTERVAL '7 days', NOW()),
(4, 'SiliconFlow 硅基流动', 'openai-chat', 'https://api.siliconflow.cn', 'CNY', true, 'healthy', NOW() - INTERVAL '7 days', NOW()),
(5, 'OpenAI 官方通道 (US-East)', 'openai-chat', 'https://api.openai.com', 'USD', true, 'healthy', NOW() - INTERVAL '7 days', NOW());

SELECT setval('channels_id_seq', 10);

-- 渠道模型白名单
INSERT INTO channel_models (channel_id, public_name, upstream_name, proxy_id, enabled, multiplier, peak_rules, supported_efforts, created_at, updated_at) VALUES
(1, 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219', 0, true, 1.0, '[]', '["low","medium","high","max"]', NOW(), NOW()),
(1, 'claude-3-5-haiku', 'claude-3-5-haiku-20241022', 0, true, 1.0, '[]', '["low","medium"]', NOW(), NOW()),
(2, 'deepseek-r1', 'deepseek-reasoner', 0, true, 1.0, '[]', '["high"]', NOW(), NOW()),
(2, 'deepseek-v3', 'deepseek-chat', 0, true, 1.0, '[]', '["off"]', NOW(), NOW()),
(3, 'gemini-2.5-pro', 'gemini-2.5-pro', 0, true, 1.0, '[]', '["low","medium","high"]', NOW(), NOW()),
(4, 'qwen-2.5-72b', 'Qwen/Qwen2.5-72B-Instruct', 0, true, 0.85, '[]', '["off"]', NOW(), NOW()),
(5, 'gpt-4o', 'gpt-4o-2024-11-20', 0, true, 1.0, '[]', '["off"]', NOW(), NOW()),
(5, 'o3-mini', 'o3-mini', 0, true, 1.0, '[]', '["low","medium","high"]', NOW(), NOW());

-- 密钥
INSERT INTO api_keys (id, name, key_hash, key_prefix, enabled, allowed_models, rate_limit_rpm, model_channels, created_at, updated_at) VALUES
(1, 'Cursor-Coding-Key', 'mockhash_cursor_prod', 'sk-rel-cur88', true, '["claude-3-7-sonnet","deepseek-r1","gpt-4o"]', 120, '{"claude-3-7-sonnet":[1],"deepseek-r1":[2,4],"gpt-4o":[5]}', NOW() - INTERVAL '5 days', NOW()),
(2, 'Claude-Code-Terminal', 'mockhash_claude_cli', 'sk-rel-cld99', true, '["claude-3-7-sonnet","claude-3-5-haiku"]', 60, '{"claude-3-7-sonnet":[1],"claude-3-5-haiku":[1]}', NOW() - INTERVAL '3 days', NOW()),
(3, 'Local-Dev-Test', 'mockhash_dev_test', 'sk-rel-dev11', true, '["deepseek-r1","gemini-2.5-pro","qwen-2.5-72b"]', 30, '{"deepseek-r1":[2],"gemini-2.5-pro":[3],"qwen-2.5-72b":[4]}', NOW() - INTERVAL '1 days', NOW());

SELECT setval('api_keys_id_seq', 10);

-- 模型单价 (model_prices)
INSERT INTO model_prices (model_name, usd_input_per1_m, usd_output_per1_m, usd_cache_read_per1_m, usd_cache_write_per1_m, rmb_input_per1_m, rmb_output_per1_m, rmb_cache_read_per1_m, rmb_cache_write_per1_m, created_at, updated_at)
VALUES
('claude-3-7-sonnet', 3.00, 15.00, 0.30, 3.75, 21.00, 105.00, 2.10, 26.25, NOW(), NOW()),
('deepseek-r1', 0.55, 2.19, 0.14, 0.55, 4.00, 16.00, 1.00, 4.00, NOW(), NOW()),
('gpt-4o', 2.50, 10.00, 1.25, 2.50, 18.00, 72.00, 9.00, 18.00, NOW(), NOW()),
('gemini-2.5-pro', 1.25, 5.00, 0.30, 1.25, 9.00, 36.00, 2.20, 9.00, NOW(), NOW())
ON CONFLICT (model_name) DO UPDATE SET
  usd_input_per1_m = EXCLUDED.usd_input_per1_m,
  usd_output_per1_m = EXCLUDED.usd_output_per1_m,
  rmb_input_per1_m = EXCLUDED.rmb_input_per1_m,
  rmb_output_per1_m = EXCLUDED.rmb_output_per1_m;

-- 插入 17 条生动丰富的真实感请求日志
INSERT INTO request_logs (
  trace_id, api_key_id, api_key_name, channel_id, channel_name,
  inbound_proto, upstream_proto, model_requested, model_upstream,
  fallback_mapped, thinking_level, is_stream, status_code, error, retry_count,
  prompt_tokens, completion_tokens, total_tokens, cached_tokens, cache_creation_tokens, reasoning_tokens,
  usage_estimated, estimated_cost, cost_currency, pricing_snapshot,
  first_byte_ms, total_ms, body_reads, last_byte_ms, upstream_ms,
  client_id, client_name, client_ua, client_ip, created_at, retry_trail
) VALUES
-- 1. Claude-3-7-Sonnet 流式深度推理 (高速生成)
(
  'tr-20261027-a001', 2, 'Claude-Code-Terminal', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219',
  false, 'high', true, 200, '', 0,
  3540, 1420, 4960, 2800, 0, 890,
  false, 0.03192000, 'USD', '{"multiplier": 1, "source": "none"}',
  620, 14250, 68, 14190, 610,
  'claude-code', 'Claude Code', 'claude-code/2.1.299 (darwin-arm64)', '120.244.18.9', NOW() - INTERVAL '3 minutes',
  '{}'
),
-- 2. DeepSeek-R1 深度思考 (人民币渠道，高首字等待)
(
  'tr-20261027-a002', 1, 'Cursor-Coding-Key', 2, 'DeepSeek 官方 API',
  'openai-chat', 'openai-chat', 'deepseek-r1', 'deepseek-reasoner',
  false, 'high', true, 200, '', 0,
  8240, 3120, 11360, 6400, 0, 2450,
  false, 0.05728000, 'CNY', '{"multiplier": 1, "source": "none"}',
  2840, 34200, 120, 34110, 2820,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '7 minutes',
  '{}'
),
-- 3. GPT-4o 极速代码补全 (非流式一次返回)
(
  'tr-20261027-a003', 1, 'Cursor-Coding-Key', 5, 'OpenAI 官方通道 (US-East)',
  'openai-chat', 'openai-chat', 'gpt-4o', 'gpt-4o-2024-11-20',
  false, '', false, 200, '', 0,
  1280, 240, 1520, 512, 0, 0,
  false, 0.00560000, 'USD', '{"multiplier": 1, "source": "none"}',
  480, 510, 1, 510, 470,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '15 minutes',
  '{}'
),
-- 4. 故障转移场景：首个渠道 429 退避，自动切到硅基流动成功
(
  'tr-20261027-a004', 3, 'Local-Dev-Test', 4, 'SiliconFlow 硅基流动',
  'openai-chat', 'openai-chat', 'qwen-2.5-72b', 'Qwen/Qwen2.5-72B-Instruct',
  false, 'off', true, 200, '', 1,
  2100, 840, 2940, 0, 0, 0,
  false, 0.00824000, 'CNY', '{"multiplier": 0.85, "source": "fixed"}',
  780, 9200, 42, 9150, 760,
  'curl', 'curl', 'curl/8.4.0', '127.0.0.1', NOW() - INTERVAL '24 minutes',
  '{"steps": [{"channel_id": 2, "channel_name": "DeepSeek 官方 API", "status_code": 429, "error": "rate limit exceeded: retry after 8s", "wait_before_ms": 500}]}'
),
-- 5. Gemini 2.5 Pro 跨协议转译入站
(
  'tr-20261027-a005', 3, 'Local-Dev-Test', 3, 'Google AI Studio',
  'gemini-generateContent', 'gemini-generateContent', 'gemini-2.5-pro', 'gemini-2.5-pro',
  false, 'medium', true, 200, '', 0,
  4520, 1890, 6410, 2048, 0, 420,
  false, 0.01510000, 'USD', '{"multiplier": 1, "source": "none"}',
  920, 16400, 74, 16320, 910,
  'cline', 'Cline', 'Cline/3.2.1 vscode/1.94', '114.248.91.3', NOW() - INTERVAL '38 minutes',
  '{}'
),
-- 6. 请求体尺寸超限 413 短路 (物理性请求错误)
(
  'tr-20261027-a006', 1, 'Cursor-Coding-Key', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'openai-chat', 'claude-3-7-sonnet', 'claude-3-7-sonnet',
  false, '', false, 413, 'request entity too large: payload 68MB exceeds maximum allowed 64MB', 0,
  0, 0, 0, 0, 0, 0,
  false, 0.00000000, 'USD', '{}',
  35, 42, 0, 0, 32,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '50 minutes',
  '{}'
),
-- 7. 上游 502 报错 (网关故障)
(
  'tr-20261027-a007', 2, 'Claude-Code-Terminal', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219',
  false, 'low', false, 502, 'Bad Gateway: upstream returned status 502 from cloudflare edge', 2,
  1420, 0, 1420, 0, 0, 0,
  false, 0.00000000, 'USD', '{}',
  180, 5200, 0, 0, 180,
  'claude-code', 'Claude Code', 'claude-code/2.1.299 (darwin-arm64)', '120.244.18.9', NOW() - INTERVAL '1 hour',
  '{"steps": [{"channel_id": 1, "channel_name": "Anthropic 官网直连", "status_code": 502, "error": "upstream connect timeout"}]}'
),
-- 8. 成功请求序列
(
  'tr-20261027-b001', 1, 'Cursor-Coding-Key', 2, 'DeepSeek 官方 API',
  'openai-chat', 'openai-chat', 'deepseek-r1', 'deepseek-reasoner',
  false, 'high', true, 200, '', 0,
  5120, 1680, 6800, 4096, 0, 1120,
  false, 0.03152000, 'CNY', '{"multiplier": 1, "source": "none"}',
  1450, 18200, 84, 18120, 1420,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '1.5 hours', '{}'
),
(
  'tr-20261027-b002', 2, 'Claude-Code-Terminal', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-5-haiku', 'claude-3-5-haiku-20241022',
  false, '', true, 200, '', 0,
  980, 420, 1400, 640, 0, 0,
  false, 0.00185000, 'USD', '{"multiplier": 1, "source": "none"}',
  240, 2450, 22, 2410, 230,
  'claude-code', 'Claude Code', 'claude-code/2.1.299 (darwin-arm64)', '120.244.18.9', NOW() - INTERVAL '2 hours', '{}'
),
(
  'tr-20261027-b003', 1, 'Cursor-Coding-Key', 5, 'OpenAI 官方通道 (US-East)',
  'openai-chat', 'openai-chat', 'o3-mini', 'o3-mini',
  false, 'medium', true, 200, '', 0,
  2400, 920, 3320, 1024, 0, 480,
  false, 0.01250000, 'USD', '{"multiplier": 1, "source": "none"}',
  850, 8900, 52, 8820, 830,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '3 hours', '{}'
),
(
  'tr-20261027-b004', 3, 'Local-Dev-Test', 4, 'SiliconFlow 硅基流动',
  'openai-chat', 'openai-chat', 'qwen-2.5-72b', 'Qwen/Qwen2.5-72B-Instruct',
  false, '', true, 200, '', 0,
  3800, 1450, 5250, 2048, 0, 0,
  false, 0.01428000, 'CNY', '{"multiplier": 0.85, "source": "fixed"}',
  460, 12400, 64, 12340, 450,
  'curl', 'curl', 'curl/8.4.0', '127.0.0.1', NOW() - INTERVAL '4.2 hours', '{}'
),
(
  'tr-20261027-b005', 1, 'Cursor-Coding-Key', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219',
  false, 'low', true, 200, '', 0,
  1650, 540, 2190, 800, 0, 120,
  false, 0.01305000, 'USD', '{"multiplier": 1, "source": "none"}',
  490, 4800, 35, 4750, 480,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '5 hours', '{}'
),
(
  'tr-20261027-b006', 1, 'Cursor-Coding-Key', 2, 'DeepSeek 官方 API',
  'openai-chat', 'openai-chat', 'deepseek-v3', 'deepseek-chat',
  false, '', true, 200, '', 0,
  6400, 1200, 7600, 5120, 0, 0,
  false, 0.01920000, 'CNY', '{"multiplier": 1, "source": "none"}',
  310, 6200, 48, 6140, 290,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '6 hours', '{}'
),
(
  'tr-20261027-b007', 2, 'Claude-Code-Terminal', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219',
  false, 'high', true, 200, '', 0,
  4200, 2150, 6350, 3100, 0, 1420,
  false, 0.04485000, 'USD', '{"multiplier": 1, "source": "none"}',
  820, 18500, 92, 18410, 800,
  'claude-code', 'Claude Code', 'claude-code/2.1.299 (darwin-arm64)', '120.244.18.9', NOW() - INTERVAL '7.5 hours', '{}'
),
(
  'tr-20261027-b008', 3, 'Local-Dev-Test', 3, 'Google AI Studio',
  'gemini-generateContent', 'gemini-generateContent', 'gemini-2.5-pro', 'gemini-2.5-pro',
  false, 'medium', true, 200, '', 0,
  3100, 980, 4080, 1200, 0, 240,
  false, 0.00877500, 'USD', '{"multiplier": 1, "source": "none"}',
  560, 9400, 50, 9340, 540,
  'cline', 'Cline', 'Cline/3.2.1 vscode/1.94', '114.248.91.3', NOW() - INTERVAL '9 hours', '{}'
),
(
  'tr-20261027-b009', 1, 'Cursor-Coding-Key', 5, 'OpenAI 官方通道 (US-East)',
  'openai-chat', 'openai-chat', 'gpt-4o', 'gpt-4o-2024-11-20',
  false, '', true, 200, '', 0,
  2800, 750, 3550, 1800, 0, 0,
  false, 0.01450000, 'USD', '{"multiplier": 1, "source": "none"}',
  420, 6800, 40, 6720, 410,
  'cursor', 'Cursor', 'Cursor/0.42.3 (windows-amd64)', '218.88.10.45', NOW() - INTERVAL '11 hours', '{}'
),
(
  'tr-20261027-b010', 2, 'Claude-Code-Terminal', 1, 'Anthropic 官网直连',
  'anthropic-messages', 'anthropic-messages', 'claude-3-7-sonnet', 'claude-3-7-sonnet-20250219',
  false, 'max', true, 200, '', 0,
  7800, 3400, 11200, 5200, 0, 2100,
  false, 0.07440000, 'USD', '{"multiplier": 1, "source": "none"}',
  1120, 28900, 140, 28810, 1090,
  'claude-code', 'Claude Code', 'claude-code/2.1.299 (darwin-arm64)', '120.244.18.9', NOW() - INTERVAL '13 hours', '{}'
);
