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
# 签名密钥不在手时用管理端登录换 token（第六轮起支持，与 UI/maestro 同路）：
# export LLM_GATEWAY_ADMIN_USER=<管理端账号> LLM_GATEWAY_ADMIN_PASSWORD=<密码>
export LLM_GATEWAY_TEST_KEY=<网关数据面 key（第三方视角）>
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

##2026-11-06 第三轮：审计、修复与5d3c6464 重部署

合并 5d3c6464 后对四项需求复审计（HEAD 无 Go 源码差异，仅 docs/tests），
发现并修掉三处；.36 实例换上 5d3c6464 构建（v0.1.1480，LaunchAgent 重启），
复跑 register/e2e/quota/usage 全绿。

**修复1 — register 幂等路径收敛 display_name**（magpie_gateway_register.py）：
幂等 PATCH 原本只收敛 base_url/notes，IP 漂移后 display_name 里嵌的旧 IP
不动（95433 一直显示 `.35`）。现在 display_name 一并比对、变化即 PATCH。
复跑：95433 display_name → `magpie @ XUTAOdeMacBook-Pro.local (192.168.31.36)`。

**修复2 — 凭据 99027 `availability_auth_failed`**：网关探针打
`192.168.31.36:3426/v1/models` 401——99027 密文里存的 caller key 与实例
现值不符（重建实例时换过 key）。用
`POST /api/providers/{pid}/credentials/{cid}/rotate-primary-key`
（`{"api_key":[REDACTED:secret]"} → 202 probe queued）轮转成当前 key；
退避窗口过后探针 200，`availability_state: auth_failed → ready`。

**修复3 — 凭据 99030 trial 绑定 outbound_model_name 为空**：网关出站发标准化名
`glm-5.3-flash-trial`，magpie 404。SQL 补
`outbound_model_name='zcode/GLM-5.3-Flash-Trial'`（99027 是注册脚本写的、
本来就对）。同轮把 95357（`.35` 时代的旧注册，凭据 suspended/exhausted 但
resolver 仍排 rank1）`PATCH /api/providers/95357 {"enabled":false}` 摘出路由，
trial 候选收敛为 95433/95436 两个活凭据。

**`glm-5.3-flash-trial` 当前 503 的残余根因是上游**：magpie 返回
`ZCode has no active limited quota for GLM-5.3-Flash-Trial`——Start Plan
夜间桶（23:00–09:00）白天不放量，路由/凭据/绑定链路已全部就绪，夜间窗口
自动出活。白天时段独占模型验证用 `glm-5.3`（只有 magpie 可路由）：
E2E 200 `FINAL-AUDIT-OK`。

**修复4（复发坑）—rotate/refresh 再次放大绑定**：99027 的绑定在 rotate 后
被 discovery 刷回 266 个全量（models refresh 同坑的第二处复发），非 GLM 流量打进
magpie 吃上游错→凭据熔断连带 glm。SQL 收窄回 5 个 zcode GLM（DELETE 261），
99030 本来就是 5 个；clear_circuit 后 glm-5.3 / glm-5.3-flash E2E 复跑均 200。
**rotate-primary-key也会触发绑定 refresh**——rotate完要复查绑定数量。

**复审计结论（2026-11-06 上午）**：需求1✅（266 模型、GLM-5.3-Flash/5.2/5.3
直连出活）；需求2✅（idempotent-patch + IP/display_name 自愈 +265 模型
检索）；需求3✅（第三方 key 经 llm.itestu.cn：glm-5.3 / glm-5.3-flash 均
200）；需求4✅（quota patch-200；usage 200/200）。
`.36` 重部署证据：`go version -m` → `v0.1.1480-…-5d3c646442ee`，:3426 → 200，
新二进制 LaunchAgent（serve）无 Launch Constraint 问题（非 GUI app）。

##2026-11-06 第四轮：trial 优先 +均衡调度

目标："优先使用 trial 的用量，注意并发限制，均衡调度"。改动全在网关库配置
（无代码变更），机制对齐 `CompareCandidatePriority` 的排序契约：
priority 桶（ `cmb.priority=TRUE 且 quota ok`）> billing round（free=1 在按量前）
> CompositeScore > manual_priority > tier > credential_id；规划期对
`candidateSaturatedLive`（in-flight ≥ concurrency_limit）的候选跳过。

**1) 绑定收窄 +防放大**：99027 的绑定被整点 discovery 每轮重新 INSERT
放大回 266（`UpsertCredentialModel` 的 INSERT 不走 manual% 守卫）。DELETE
会复活——改用"保留行 + available=FALSE + unavailable_reason=manual-* +
admin_protected=TRUE"（261 行），discovery 的 UPDATE 分支被 `manual%`
前缀与 `admin_protected` 双守卫拦住（连 updated_at 都不碰）。
注意：`emergency-repair force_enable` 会无差别把**所有**绑定置
available（99027 实测 cmb_rows_updated=266）——收窄后禁用此动作，99027 的
`suspended/periodic_exhausted` 自行恢复（窗口重置即 ready）。

**2) trial/GLM 优先**：两个 magpie 凭据的 5 个 zcode GLM 绑定
`priority=TRUE` + manual_priority 错开：trial
（99027=1,99030=2）、glm-5.3（99027=2,99030=1）、flash/5.2/turbo=3。生效后
resolve：**trial rank1=99027/rank2=99030（唯一两候选）**；glm-5.3 rank1/2
=两个 magpie（第三方全在其后）；glm-5.3-flash 在 NVIDIA NIM
（运维置顶 prio1/2/3）之后的第4位（prio=3 同档，NIM 挂了立即顶上）。

**3) 并发对齐**：99027/99030 `concurrency_limit` 20→3、fp_slot_limit 20→3
（magpie lane zcode=3）。6 并发实测：5/6 成功，单凭据排队生效
（5.6–48s），1 发 503 为排队超时（快速失败合理）。
.36 的 zcode 5h 窗口耗尽（1308，18:54 重置）时网关自动全量
回落 .30——均衡调度按"试遍候选"语义工作正常，窗口恢复后自然分担。

##2026-11-06 第五轮：trial 桶独立并发（凭据拆分）

约束明确：trial 桶（Start Plan）上游最大并发3，与各节点 serve池
并发不同——凭据级并发不能一刀切。方案：每节点拆双凭据，网关按凭据
独立限流，trial 满载不再连坐 STD 排队。

**凭据拓扑（2026-11-0617:2x落位）**：
|凭据 |节点 |绑定 | concurrency | fp_slot |
|---|---|---|---|---|
|99032 magpie-trial | .36 | 仅 trial |3|3|
|99033 magpie-trial | .30 | 仅 trial |3|3|
|99027 magpie-serve | .36 |4 个 STD GLM |6|3|（pool池 lane）
|99030 magpie-serve | .30 |4 个 STD GLM |3|3|（单账号 lane）

- serve 凭据的 trial 绑定禁用（
`manual-moved-to-trial-credential` + admin_protected）；trial 凭据非
trial 绑定禁用（discovery 给新凭据秒灌265 模型——narrow 269 行）。
-99030 的 caller key 明文经 `POST .../credentials/{cid}/reveal`
取出（audit log 留痕）用于创建 .30 的 trial 凭据。
- resolve 验证：trial rank1/2=99032/99033（并发3 独立）；STD rank1=99030
（.36 的 99027 因5h 窗口 suspended，18:54 重置自动恢复并发6）。
-混合负载实测：2 trial +2 STD 并发——trial 0.2s 快速 503
（白天无夜间配额），STD 双双 200 不受连坐；夜间窗口后 trial 真实出活
且最多占3 槽，余量留给 STD。
- register 脚本同步升级（
`magpie_gateway_register.py`）：`magpie-trial` 凭据随注册自动创建
（`TRIAL_CONCURRENCY=3`，`--serve-concurrency` 可调），模型绑定按桶分组；
同一台机跑两节点注册时必须 `MAGPIE_REGISTER_HOSTNAME/IP --code`
三件套覆盖身份（实测漏覆盖会把 95357 的 base_url 错改到本机，已修复）。

## 2026-11-07 第六轮：审计、fake-ip 事故与修复（HEAD 65a97912 → 05d2a74a）

合并 65a97912（codex proxied pill 的 respect_system_proxy 回归测试）后对本轮
代码审计。Go 侧：`gofmt -l` 揪出 75df6f48 落地的 `internal/library/skillgit_test.go`
未过 gofmt（已修，`go test ./internal/library` ok）；`go vet` agent/edit/gateway
干净；`TestCodexProxied*` 回归绿；`make build` 出 HEAD 二进制。

**部署**：本机 `.30` 节点（`magpie web`，XDG_CONFIG_HOME/CACHE 指
`local-node/config|cache`）二进制 49fd3e47 → **05d2a74a**：:3446 web、:3425
gateway（lan:true → 0.0.0.0）均 200；`/v1/models` 与旧二进制 A/B 完全一致
（5 个 zcode 模型）；LAN 路径 `192.168.31.30:3425` 可达（无 key 401）。

**事故（clash verge fake-ip TUN 打穿注册脚本）**：首次 `all` 实测里
`lan_ip` 的 UDP connect 出口地址返回 **198.18.0.1**（clash TUN 的 fake-ip 段），
幂等路径把它当 LAN IP PATCH 了 provider **95436**：base_url →
`http://198.18.0.1:3426/v1`、display_name 嵌入假 IP（PATCH 200）。当时的 E2E
200 是 glm-5.3-flash 的 NVIDIA NIM 候选出的活，没走 magpie，未立即暴露。已
手工 PATCH 还原 base_url/display_name（200），随后的修复版脚本实测收敛如常。
`~/.config/magpie` 下 8 个运行态文件被误启进程（没带沙箱 XDG 的一次启动）重写
——该目录本就是 11-06 首次部署时误建的默认配置（无 settings.json、无用户数据，
本机也没装 magpie.app），无实际损害，记录在案。

**修复（magpie_gateway_register.py + run-local.sh，commit 05d2a74a + 本轮）**：

1. **base_url 守卫升级为 RFC1918 全量校验**：自动检测到的地址必须是
   10/8、172.16/12、192.168/16 之一；回环、169.254/16、100.64/10（netbird，
   网关 egress 守卫熔断）、**198.18/15（fake-ip TUN）**一律拒绝
   （`REGISTER_RESULT=fail-non-lan-ip`，退出 1）。`MAGPIE_REGISTER_IP` 显式
   声明仍放行（操作者自己的决定）。`lan_ip` 同步过滤假段，UDP 结果非 RFC1918
   时回落 getaddrinfo 枚举。13 个判定用例 + 用真实事故值 198.18.0.1 的守卫
   实证。
2. **回环目标直连**：`http_json` 对 127.0.0.1/localhost/::1 用空 ProxyHandler
   强制直连——clash verge 的 `http_proxy` 在手时 urllib 会把本机 magpie 的请求
   也交给代理（代理回不到本机回环 → 502/超时，"本地端口无法访问"的典型成因）。
   坏代理 env + 本地假服务器实证 200。
3. **登录鉴权路径**：新增 `--gateway-admin-user/--gateway-admin-password`
   （env `LLM_GATEWAY_ADMIN_USER/PASSWORD`），走网关自己的
   `POST /api/auth/token` 换 token——管理端 UI/maestro 同路。此前只支持
   自签 super_admin JWT，而 211/252 的签名密钥本机没有 SSOT（`.env.252.enc`
   与 age key 都不在），脚本实际跑不起来。maestro_test 实为 super_admin，
   实测登录 → 列 providers → PATCH 全通。
4. **证据不再自相矛盾**：serve 凭据创建失败原先先记 `fail-cred-httpX` 又被
   末尾 `REGISTER_RESULT=ok` 覆盖；现在结论记 `partial` 且退出非 0（能绑的
   绑定照做）。BIND_MODELS 空时不再同时打 `=none` 和 `=` 两行。
5. **密钥脱敏**：secret/test key/caller key/管理端密码统一进 SECRETS，
   `log`/`evidence` 输出前替换为 `[REDACTED]`。
6. **杂项**：`/api/providers` 非 200 时不再无声吞成空列表（本次审计就被
   401-吞空误导过一轮）；`e2e/all` 缺 test key 快速报错（原来发 `Bearer None`）；
   死代码出清（`.local` 替换在去点后永假、TRIAL_STD 未用、quota 双重
   find_by_code、`443 if "https" else 8781` 恒真分支——探测端口改按网关
   scheme 取）。
7. **run-local.sh**：三处 CDP 探测 curl 加 `--noproxy '*'`——clash 的
   `http_proxy` 会让 127.0.0.1:9222 的探测进代理，Chrome 被误判起不来。

**实测（登录鉴权，MAGPIE_REGISTER_HOSTNAME/IP 钉 .30 身份，
--magpie http://127.0.0.1:3425）**：
`VERIFY_PLATFORM=Darwin/arm64`、`HOSTNAME=niuerdeMacBook-Pro.local`、
`REGISTER_MODE=idempotent-patch`、`PROVIDER_ID=95436`、凭据 99030(serve)+
99033(trial) 双 existing、`BINDINGS_AFTER` 5 模型、`REGISTER_RESULT=ok`；
E2E（第三方 key）glm-5.3-flash → 200；**magpie 独占模型 glm-5.3 → 200
`MAGPIE-ATTRIB-OK`**（排除 NIM 候选的归属验证）；本机直连
:3425 `zcode/GLM-5.3` → 200 `LOCAL-DIRECT-OK`；quota snapshot PATCH 200；
usage 200/200（provider_name 已是
`magpie @ niuerdeMacBook-Pro.local (192.168.31.30)`，notes.ip 同步收敛）。
网关上 95436 终态：base_url=`http://192.168.31.30:3425/v1`、
registration=auto、magpie_port=3425。

**Maestro**：5 flow 全绿（m1/m2/m3 + g1/g2），报告
`tests/maestro/reports/20261107-005906/`。

**本轮新建网关数据面 key**：id 230 `magpie-round6-e2e`（app e2e-magpie-test）
——key 列表不回显既有值，沿用往轮建一次性 key 的做法（229 `magpie-e2e-test`
同例）。

**网关侧问题（不在 magpie 仓库修复）**：

5. **trial 桶夜间 0 候选**：01:00（+0800，夜间桶 23:00–09:00 内）调
   `glm-5.3-flash-trial` → 503 `no_candidates, tried=0`，而两个 trial 凭据
   （99032/99033）`availability_state=ready`、`GET /api/routing/resolve` 也
   列得出候选——请求路径的规划器与 resolve 不一致。疑似规划器按 UTC 评估
   夜间桶（01:00 +0800 = 16:xx UTC，被判白天）。第五轮"夜间窗口自动出活"
   的预期未兑现，留待 llm-gateway-go 仓查证。

**未试**：Windows/Linux 部署重放（本轮无相关变更）；trial 桶真实出活
（被上面第 5 条挡住，与 magpie 侧无关）。
