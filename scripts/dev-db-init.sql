-- 本地 dev 数据库初始化：创建与 config.example.yaml 同口径的角色与库。
-- 由搭建脚本以 postgres 超级用户身份执行（psql -f dev-db-init.sql）。
-- 幂等：重复执行不会报错（角色/库已存在时跳过）。
SELECT 'CREATE ROLE llmrelay LOGIN PASSWORD ''llmrelay-dev'''
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'llmrelay')\gexec

SELECT 'CREATE DATABASE llm_relay OWNER llmrelay'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'llm_relay')\gexec

ALTER ROLE llmrelay WITH LOGIN PASSWORD 'llmrelay-dev';
ALTER DATABASE llm_relay OWNER TO llmrelay;
