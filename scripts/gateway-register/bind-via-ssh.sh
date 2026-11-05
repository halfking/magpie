#!/bin/bash
# bind-via-ssh.sh — 42P08 网关 bug 的 SQL fallback：为一台 magpie 主机的 credential 直插模型绑定
# 用法: bind-via-ssh.sh <credential_id> <model1,std1> <model2,std2> ...
# 例:   bind-via-ssh.sh 99028 zcode/GLM-5.3,glm-5.3 zcode/GLM-5.2,glm-5.2
set -euo pipefail
CID=$1; shift
VALUES=""
for pair in "$@"; do
  raw="${pair%%,*}"; std="${pair##*,}"
  VALUES="${VALUES:+$VALUES,}('$raw','$std')"
done
ssh -o BatchMode=yes -o ConnectTimeout=10 root@192.168.31.211 "docker exec acc-pg17 psql -U postgres -d llm_gateway -c \"
WITH cred AS (SELECT provider_id, plan_type FROM credentials WHERE id = $CID),
upsert_pm AS (
  INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name, canonical_id, standardized_name, outbound_model_name, available, source, last_seen_at)
  SELECT cred.provider_id, m.raw, m.raw, NULL, m.std, m.raw, TRUE, 'manual', NOW() FROM cred, (VALUES $VALUES) AS m(raw,std)
  ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET standardized_name = EXCLUDED.standardized_name, outbound_model_name = EXCLUDED.outbound_model_name, source='manual', last_seen_at=NOW(), available=TRUE, updated_at=NOW()
  RETURNING id, standardized_name
)
INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, routing_tier, weight, manual_priority, success_rate, p95_latency_ms, billing_mode, plan_type_origin, admin_protected, context_window_override, context_window_source, context_window_updated_at)
SELECT $CID, upsert_pm.id, TRUE, 2, 100, 99, 0.9, 0, CASE WHEN cred.plan_type='token' THEN 'per_token' ELSE COALESCE(cred.plan_type,'per_token') END, 'manual', TRUE, 128000, 'manual', NOW()
FROM upsert_pm, cred
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET available=TRUE, admin_protected=TRUE, updated_at=NOW()
  RETURNING id;\""
