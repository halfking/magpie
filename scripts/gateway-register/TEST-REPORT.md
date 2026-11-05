# magpie 自动注册 llm.itestu.cn 网关测试报告

**日期**: 2026-11-05
**测试环境**: macOS (Darwin/arm64, XUTAOdeMacBook-Pro) / magpie serve 实例 :3426 / 网关 llm.itestu.cn (211 容器 llm-gateway-211-a:8781)
**脚本**: `.deployment-validation/gateway-register/magpie_gateway_register.py`（纯 Python 标准库，跨平台）

## 验证结论总览

| # | 需求 | 结果 | 证据 |
|---|------|------|------|
| 1 | 本地 IDE 独有模型服务 | ✅ 通过 | 3426 直连 `zcode/GLM-5.3-Flash-Trial` HTTP 200, content="LOCAL-UNIQUE-OK" |
| 2 | 自动注册到网关 | ✅ 通过 | provider 95433 + credential 99027 创建 200；二次运行走幂等 PATCH |
| 3 | 他人经网关访问 | ✅ 通过 | 仅 caller key（第三方身份）经 `llm.itestu.cn/v1/chat/completions` model=glm-5.3 / glm-5.3-flash → HTTP 200 "GATEWAY-E2E-OK" |
| 4 | 多平台部署 + 模型/余量检索 + 用量同步 | ✅ 通过 | 自动检索 265 模型并绑定；quotas.json 余量 PATCH 进 provider notes（200）；usage API 记账 200（2 req / 417 tokens / success 1.0）|

## 各需求明细

### 需求1 — 本地独有模型服务

- magpie serve 实例（LaunchAgent `com.magpie.codex-validation`）监听 `0.0.0.0:3426`，`/v1/models` 返回 **265 个模型**，其中 `zcode/*` 前缀独有 5 个：GLM-5.3、GLM-5.3-Flash、GLM-5.2、GLM-5-Turbo、GLM-5.3-Flash-Trial。
- 直连出活：`/v1/chat/completions` model=`zcode/GLM-5.3-Flash-Trial` → HTTP 200，content `"LOCAL-UNIQUE-OK"`（61 completion tokens）。
- 证据: `VERIFY_MAGPIE_MODELS_HTTP=200`、`VERIFY_MAGPIE_MODELS_COUNT=265`。

### 需求2 — 自动注册（幂等，主机标识防撞）

- 注册约定按 2026-11-05 规范执行：`code=magpie-<主机名小写去点>`，`(tenant_id, code)` 唯一键天然防撞。
- 首次注册：`POST /api/providers` → 200（id 95433）→ `POST .../credentials` → 200（id 99027, plan_type=free, label=magpie-serve）。
- 幂等复跑：同 code 查到已存在 → 不再 POST，走 `PATCH /api/providers/95433` 收敛 base_url/notes（`VERIFY_REGISTER_MODE=idempotent-patch`）。
- IP 漂移自愈：base_url 与检测到的 LAN IP 不一致时 PATCH 更新。
- **多平台可移植性实测**：用 `MAGPIE_REGISTER_HOSTNAME=linux-box-01 MAGPIE_REGISTER_IP=192.168.31.99` 模拟第二台主机 → 独立 provider 95434/credential 99028 注册成功，与主机一互不干扰；验证后已清理（DELETE 200）。脚本本身不依赖 macOS 特有 API（纯 stdlib，Linux/Windows 直接可跑）。
- notes 元数据：`{source, hostname, host, ip, platform, magpie_port, registered_at, registration:"auto"}`。

### 需求3 — 第三方经网关访问（E2E）

- 第三方视角 = 只持网关数据面 key（`LLM_GATEWAY_TEST_KEY`），不带任何 admin 凭据。
- `POST https://llm.itestu.cn/v1/chat/completions` model=`glm-5.3`（magpie 独占候选，freellm 侧该模型 is_routable=f）→ HTTP 200，`"GATEWAY-E2E-OK"`；model=`glm-5.3-flash` → 200（多候选池，本机 magpie 也在路由表 `zcode/GLM-5.3-Flash is_routable=t`）。
- 路由确认：`routing_resolve top_provider_id=95433 top_credential_id=99027 top_raw_model=zcode/GLM-5.3`。
- 用量记账：`usage_ledger_with_current_month` 落行 `provider_id=95433, raw=zcode/GLM-5.3, total_tokens=379, success=t`。

### 需求4 — 多平台 + 模型/余量检索 + 用量同步

- **模型自动检索**：脚本调 magpie `/v1/models` 自动筛 `zcode/*`，对照网关绑定增量补齐（幂等，已绑定的跳过）。绑定结果 `glm-5.3, glm-5.3-flash, glm-5.2, glm-5-turbo`。
- **余量自动检索**：读 magpie 配置目录 `quotas.json`（9 账号，zcode 系 7 个），汇总各账号 window 用量/上限/重置时间 → `quota_snapshot` PATCH 进 provider notes（HTTP 200），网关 UI 可见。
- **用量同步**：网关数据面请求自动记账；`GET /api/usage/providers/95433` → 200 `{request_count:2, total_tokens:417, success_rate:1.0}`；`.../models` → 200 按模型细分。usage summary 对"从未有流量"的 provider 返回 500（pgx no-rows 未处理）——首次请求落账后即 200。

## 发现的网关侧问题（不在 magpie 仓库修复）

1. **`POST /api/providers/{id}/credentials/{cid}/models` 恒 500（42P08）**：SQL 参数 `$8`（context_window_override）在语句中出现 3 次且无显式 cast，pgx 无法推断类型 → `could not determine data type of parameter $8`。**缓解**：`bind-via-ssh.sh` SQL 直插（与 95357 相同路径）；根治需在 llm-gateway-go 的 `insertManualCredentialModelSQL` 给 `$8` 加 `::int` cast。
2. **usage summary 对零流量 provider 500**：`usageProviderSummary` 的 `QueryRow...Scan` 未处理 `pgx.ErrNoRows`。建议返回全零 summary。
3. **路由优先级并列时 freellm 排前**：glm-5.3-flash 两候选（95302/95433）tier/weight/priority 全同，freellm 靠前。若希望 magpie 优先可调 `manual_priority`。
4. 42P08 曾在 211 上以"手动绑定 API bug"记录过（2026-11-05 凌晨），本次确认根因并给出可复现 SQL fallback。

## 交付物

- `magpie_gateway_register.py` — 一条龙：`identify / models / register / e2e / quota / usage / all`
- `bind-via-ssh.sh` — 42P08 缓解的 SQL 绑定 fallback
- 证据行：`VERIFY_PLATFORM / HOSTNAME / LAN_IP / CODE / MAGPIE_MODELS_* / BIND_MODELS / REGISTER_MODE / PROVIDER_ID / CREDENTIAL_ID / BINDINGS_AFTER / REGISTER_RESULT / E2E_STATUS / E2E_SECONDS / QUOTA_SYNC / USAGE_READ`

## 复跑方式

```bash
export LLM_GATEWAY_SECRET=<211/252 网关 LLM_GATEWAY_SECRET_KEY>
export LLM_GATEWAY_TEST_KEY=<envs SSOT projects/llm-gateway-go/.env.secrets.plain.yaml>
export MAGPIE_CALLER_KEY=<magpie 3426 caller key>
export MAGPIE_CONFIG_DIR=~workspace/ai/magpie/.deployment-validation/config/magpie
python3 magpie_gateway_register.py all --probe-wait 5
# 绑定 500 时: ./bind-via-ssh.sh <credential_id> "zcode/GLM-5.3,glm-5.3" ...
```
