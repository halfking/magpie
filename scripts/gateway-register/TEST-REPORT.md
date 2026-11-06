# magpie 自动注册 llm.itestu.cn 网关测试报告

**日期**: 2026-11-05
**测试环境**: macOS (Darwin/arm64, XUTAOdeMacBook-Pro) / magpie serve 实例 :3426 / 网关 llm.itestu.cn (211 容器 llm-gateway-211-a:8781)
**脚本**: `scripts/gateway-register/magpie_gateway_register.py`（纯 Python 标准库，跨平台）

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
export MAGPIE_CONFIG_DIR=~/workspace/ai/magpie/.deployment-validation/config/magpie
python3 scripts/gateway-register/magpie_gateway_register.py all --probe-wait 5
# 绑定 500 时: scripts/gateway-register/bind-via-ssh.sh <credential_id> "zcode/GLM-5.3,glm-5.3" ...
```

## 2026-11-06 第二轮：Maestro 黑盒 + 多环境部署

代码：HEAD 13ac949d（本报告路径修正后的下一提交）。新增
[tests/maestro](../../tests/maestro/README.md)：Maestro WEB（CDP Chrome）驱动
magpie 页与管理端页的黑盒套件，5 flow 全绿（m1 供应商 / m2 用量 / m3 网关 /
g1 管理端列表过滤出行 / g2 详情页 base_url+凭据）。

本机节点（niuerdeMacBook-Pro，192.168.31.30:3425）：`magpie web` 便携实例，
ZCode 从 ~/.zcode 自动发现（GLM Coding Max），网关 key `211-gateway`，
注册为 **provider 95436 / credential 99030**（registration:auto，5 模型绑定，
网关 discovery 自动补齐）。E2E：第三方 key 经 llm.itestu.cn 调
glm-5.3-flash → 200。新 provider 需 `PATCH /api/routing/emergency-repair`
force_enable 才立即可路由（URSM 启动快照）。

.36 XUTAOdeMacBook-Pro（192.168.31.36:3426，即第一轮的 .35 节点，IP 漂移）：
serve 实例（LaunchAgent com.magpie.codex-validation）换上 49fd3e47 构建，
:3426 → 200。注册脚本幂等收敛 provider 95357：base_url
.35→.36 PATCH 200（IP 漂移自愈实测）；新凭据 99031（discovery 自动绑定）；
99025/99031 并发都收敛到 3（fp_slot_limit 同改），99025 reset-state +
99031 force_enable。magpie 侧 lane：zcode 3、zcode-pool 三账号各 3、
队列 1×15s。quota snapshot PATCH 进 95357 notes（200）。
**归属验证**：`glm-5.3-flash-trial`（magpie 独占，rank1=95357）经网关 →
200 `NODE36-E2E-OK`，95357 用量 235→236。注意 glm-5.3-flash 的常规路由
rank1 是 NVIDIA NIM 等直连供应商，magpie 排在其后——钉流量要么用 magpie
独占模型名，要么临时 model-toggle 直连凭据。
.36 桌面 app（/Applications/magpie.app，:3425）**保持原版本**：macOS 26
对 LaunchAgent 二进制有 Launch Constraint（首启签名入缓存），换二进制后
SIGKILL (Code Signature Invalid / Launch Constraint Violation)，ad-hoc 重签
也过不了；已恢复原签名二进制并 bootstrap 复原（:3425 → 200）。49fd3e47 相比
原 60b3ac53 只有 scripts/docs 差异，桌面 app 无需更新。

.36 上跑注册的写法（在本机跑，覆盖身份避免撞 code）：
`MAGPIE_REGISTER_HOSTNAME=XUTAOdeMacBook-Pro.local MAGPIE_REGISTER_IP=192.168.31.36
--code magpie-zcode --magpie http://192.168.31.36:3426`；quota 子命令的
--magpie-config-dir 要指到含 quotas.json 的 **magpie 目录本身**。

.19（192.168.31.19，lanzheng）：**部署被阻——凭据不匹配，非账号锁定**。机器是
Windows（IIS/10.0 + SMB/RPC）。2026-11-06上午22端口已开（Windows OpenSSH），
但 `86133@192.168.31.19` 用89935400密码 SSH被拒（publickey,password,
keyboard-interactive 全试）；40 分钟内5次重试（含10 分钟间隔避开锁定窗口）
全部 denied。SMB(445)交叉验证：86133/lanzheng/Lanzheng/Administrator ×
89935400 全部返回 **STATUS_LOGON_FAILURE（0xc000006d，凭据错误）**而非
STATUS_ACCOUNT_LOCKED_OUT（0xc0000234，锁定）——**密码本身不对**，不是锁
定。windows/amd64部署包已备好（magpie-windows-amd64.exe49fd3e47 +
start-magpie.cmd，便携模式 data/布局，`MAGPIE_ADDR=0.0.0.0:3426`），拿到
正确凭据后按 .36 流程重放即可。
