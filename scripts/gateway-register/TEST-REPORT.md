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
`tests/maestro/reports/20261107-005906/`；最终 head（801b258a，Go 树与部署
二进制完全一致，diff 为空）上复跑再绿一轮：`reports/20261107-010540/`；
与 origin be870f2be（上游 v0.1.3189 同步）合并后的最终 head（95a758d6f）重建
重部署后再绿一轮：`reports/20261107-011225/`，glm-5.3 e2e 200 `FINAL-HEAD-OK`。
（合并前后 `gofmt -l` 全净、`go build ./...` ok；上游未触碰
skillgit_test.go，第六轮的 gofmt 修复保持。）

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
##2026-11-07 第六轮-B：.19（Windows/yonyou）首次落地 + 审计修复 + Maestro 全绿

（上节第六轮的「未试：Windows/Linux 部署重放」由本节兑现。）

第二轮被 SSH 凭据阻塞的 .19（192.168.31.19，Windows 10，hostname=yonyou）改为
**本机直接部署**（无需 SSH），从 65a97912+审计修复构建，注册/网关 E2E/UI 黑盒
全部打通。

**部署拓扑**：`C:\workspace\magpie\.deployment-validation\magpie-node\`（便携
模式，`.portable` + `data\`），`MAGPIE_WEB_KEY` + `MAGPIE_ADDR=0.0.0.0:3426`
+ `magpie web --addr 0.0.0.0:3446 --no-open`，settings.json `{"lan":true,
"lang":"zh"}`。ZCode 登录态（bigmodel Start Plan，GLM Coding Lite）启动 20s
后由 `KeepRetiringMoved` 自动迁到插件 `@magpie-community/opencode-zcode-auth`
（0.1.51→0.1.52 自动升级），/v1/models 25 个、独有 5 个 `zcode/*`。共享网关
key `211-gateway`。本地直连出活：`zcode/GLM-5.3` → 200 `LOCAL-UNIQUE-OK`。

**Windows 防火墙坑（新，记档）**：节点首启弹的防火墙提示无人应答，Windows
为该 exe 自动生成两条**程序级 Block** 规则（Public 档，TCP+UDP）——Block 压过
Allow，211 → .19:3426 超时（22 端口有 sshd 放行规则可通，对照定位）。进程
存活时删 Block 规则会立刻重生；正确顺序：**停进程 → 删 Block → 建 Allow
（程序级）→ 再启动**。修复后 211 → .19:3426/v1/models 200。

**审计修复（本轮 9 处）**：
1. `magpie_gateway_register.py` 死条件 `443 if "https" else 8781`（恒真）；
2. **key 漂移自愈**：既有凭据 `availability_state=auth_failed` 时幂等复跑
   自动 `rotate-primary-key` 到当前 caller key（第三轮 99027 的坑，需在绑定
   之后轮转——rotate 探针要求已绑模型名）；
3. `cmd_quota` 双重 `find_by_code`（两次 API）；
4. 绑定 API 500 时给出 42P08 SQL fallback 指引；
5. e2e 网络错误 evidence 区分 `err`/HTTP code；
6. **quota 来源兜底**：quotas.json 未落盘时（keepLast 对含 pool 窗口的账号
   跳过写盘，本机实测如此）自动改读 `/v1/magpie/quotas`，`remaining` 直取；
7. `cmd_all` 不再依赖 `--magpie-config-dir` 才同步 quota；
8. `run-local.sh` Windows 适配（JDK/Chrome 查找 + maestro PATH 兜底）；
9. `internal/library/skillgit_test.go` gofmt 漂移（code-standards 验收项）。

**注册**：provider **95464**（code=magpie-yonyou）+ 凭据 **99034 magpie-serve**
（并发3）/ **99035 magpie-trial**（并发3），availability_state 均 ready（探针
200）。discovery 给新凭据秒灌全量目录且 outbound_model_name 为空（第五轮坑
重现）——SQL 收窄：99034 只留 4 个 STD GLM、99035 只留 trial，outbound 补齐
`zcode/*`，`manual-*` + `admin_protected` 防复发。**emergency-repair
force_enable 是按凭据作用域**（本轮从网关源码确认，`credential_id` 必填），
但它会把该凭据全部绑定置 available（cmb_rows_updated=25）——force_enable
之后再收窄一次即可，勿对收窄过的老凭据使用。

**网关 E2E（第三方视角）**：新签发数据面 key（id 231，`POST /api/keys` 直接
返回明文）经 `llm.itestu.cn/v1/chat/completions` model=`glm-5.3-flash-trial`
→ **200 "NODE19-GATEWAY-E2E-OK"**（12.6s）。归属铁证=网关容器 audit 日志：
`upstream_url=http://192.168.31.19:3426/v1/chat/completions provider=95464
credential=99035 upstream_status=200 success=true`（resolve top 是 .36 的
99032，活体检查跳过后 attempt=0 实发 .19）。复跑 1×200（4.7s）；中间两次
503/失败为候选失败冷却（transient）。quota snapshot PATCH 进 95464 notes
（200，来源=/v1/magpie/quotas：5h 74/2000、Weekly 10000/10000 至 11-11、
trial 周末桶 21.5M/500M）。

**网关侧残留问题（不在本仓库修）**：
- usage_ledger_hot 的归属字段记的是 **resolve top 候选**而非实际服务凭据
  （audit 日志 95464/99035，热表行 95433/99032，时延逐毫秒一致）→
  `/api/usage/providers/95464` 仍按零流量 500（已知 no-rows bug 叠加此归属
  偏差）；usage by models 200（空）。
- 绑定 API 42P08（$8 无 cast）依旧，SQL fallback 可用。

**Maestro 黑盒（tests/maestro，本机 Windows 首跑）**：5 flow 全绿
（m1 供应商 / m2 用量 / m3 网关 / g1 管理端列表 / g2 详情页）。
变基落位后（ca3e57f94+本提交=4cf1ff929）在推送头重建的节点上**复跑
5/5 全绿**（reports/20261107-023028，EXIT=0）；注册/路由/直连 trial 出活
同点复验（95464 enabled、registration=auto、resolve 含 95464 双凭据）。
Windows 适配与修复：
- `--driver-host-port` 在 Windows 不可用：maestro 对该端口做 **ServerSocket
  试绑探测**，Chrome 占 127.0.0.1:9222 时 wildcard 绑定 EADDRINUSE（macOS 靠
  SO_REUSEADDR 放行，Java 在 Windows 不开）。run-local.sh 默认不传该参数
  （WEB 的 CdpClient 本就默认 localhost:9222），旧行为用
  `MAESTRO_PASS_DRIVER_PORT=1` 显式开。
- 管理端测试账号需 **super_admin**（供应商列表端点 403 tenant_admin）；
  本轮建 `magpie-node19`（id 46）。
- g1 两处随环境漂移的断言修正：页面语言随 profile（新 CDP profile 英文）
  ⇒ 标题/占位改中英 alternation；管理端搜索是 display_name **前缀匹配**
  且 v2.5.9 把 notes JSON 渲染进单元格（行高巨大，'magpie' 过滤出的多行里
  我们的行垫底滚不出）⇒ 过滤词改 `magpie @ <host>`，只剩我们的行且在最前。
- m1 套餐名随账号（Max/Lite）⇒ 断言 `GLM Coding.*` 前缀。
- `.env.local` 必须 shell 语法（bash source），env.yaml 的 YAML 形状只是清单。

**复跑方式**（本机）：`.deployment-validation\magpie-node\` 下
`MAGPIE_WEB_KEY=$(cat webkey.txt) MAGPIE_ADDR=0.0.0.0:3426 ./magpie.exe web
--addr 0.0.0.0:3446 --no-open`；`tests/maestro/.env.local` 就位后
`bash tests/maestro/run-local.sh`；注册/E2E：
`python scripts/gateway-register/magpie_gateway_register.py all --magpie
http://127.0.0.1:3426`（LLM_GATEWAY_SECRET / LLM_GATEWAY_TEST_KEY /
MAGPIE_CALLER_KEY 见 .deployment-validation 下 gitignored 文件）。

##2026-11-07 第七轮：.19 节点正式安装 + 登录自启

验证节点从 `.deployment-validation` 提升为**正式安装**（数据/注册态原样保留）：

**安装布局**：`C:\Users\86133\AppData\Local\Programs\magpie\` —— `magpie.exe`
（windowsgui 子系统构建，登录自启不弹控制台；版本 28750b102）+ `data\`
（便携 data 整体迁入，`.portable` 随行）+ `magpie-cli.exe`（console 构建，
终端管理用，同一 data）+ `webkey.txt` / `gateway-key-211-gateway.txt`。
settings.json 补 `"port": 3426`（与 `lan:true` 一起让网关免 MAGPIE_ADDR 绑
0.0.0.0:3426——`gateway.listenAddr()` 的行为）。

**登录自启**（Windows 上 `magpie daemon install` 不支持，机制是 autostart）：
- `magpie autostart on`（安装位 exe 执行）→ HKCU Run 值 `magpie` =
  `"<exe>" tray`（登录进托盘、带网关；Task Manager 启动项可见可关）。
- 管理页另建 HKCU Run 值 `magpie-web` = `"<exe>" web --addr 0.0.0.0:3446
  --no-open`；web 密钥经 `setx MAGPIE_WEB_KEY`（用户环境变量，Run 键带不了
  env；不设则每次随机、链接与 Maestro 的 MAGPIE_URL 都会漂）。
- 网关先到先得：tray 与 web 两个进程同数据目录，谁先起谁服务网关，另一方
  让位（web_cli 的 "another magpie has the gateway" 路径），行为已实测。

**验证**（安装位）：0.0.0.0:3426+3446 监听 ✓；/v1/models 25 模型/5 zcode ✓；
从 211 探测 200 ✓；**远程鉴权边界**：无鉴权/错 key 的 chat 从 211 打 → 401
（本机 loopback 免鉴权是 magpie 设计——密钥管远程调用者）；网关 E2E → 200
`INSTALLED-AUTOSTART-OK`（audit：provider=95464 credential=99035）✓；
`magpie-cli autostart` → "opens at login, in the tray" ✓。
下次登录起自动拉起；重启后无需人工介入。

## 2026-11-07 第八轮：.30/.36 Mac 节点按最新 HEAD 重部署 + 48h 新功能的 Maestro 覆盖

HEAD fcd829a52（构建 `v0.1.3190-0.20261106184357-fcd829a5212e`，`make build`）。
本机（IP 又从 .30 漂回 **.36**）全新便携节点 `.deployment-validation/round8-node/`
（XDG 沙箱 config/cache，`settings.json {"lan":true,"lang":"zh","port":3427}`）：
web `:3446`（MAGPIE_WEB_KEY，cookie 流 303→200）、网关 `:3427` 绑 `*:3427`（tcp46）。
启动即见 `moved zcode to its plugin`；/v1/models 13 个、独有 5 个 `zcode/*`；
LAN 边界：无 key 401 / 有 key 200；直连 `zcode/GLM-5.3` 出活（200）。

**坑（新）**：settings 的 `lang` 合法值是 `system|en|zh|zh-TW|ja|de`
（internal/settings Langs）——写 `"zh-CN"` 被校验拒掉，UI 落回英文，Maestro
中文断言全线空match。改 `"zh"` 重启即正。tests/maestro/README 的写法已同步纠正。

**注册**：provider **95436** 幂等 PATCH：base_url `http://192.168.31.30:3425/v1`
→ `http://192.168.31.36:3427/v1`（IP+端口双漂移，PATCH 200），display_name 收敛
`magpie @ niuerdeMacBook-Pro.local (192.168.31.36)`，registration=auto；凭据
99030(serve)/99033(trial) existing，绑定 5/5。**新坑（轮）**：节点换端口重建后
凭据密文里的旧 caller key（尾号 b4e2）在网关侧还没被探针标成 auth_failed
（状态是漂移前的陈 ready），注册脚本的漂移自愈没触发——网关请求以 401 打到
新 base_url（节点日志三连拒）后回落旧候选，前两次 E2E 200 的归属因此存疑。
手动 `rotate-primary-key`（202，probe queued）后**数据面仍持旧 key**（URSM
内存快照）——再按凭据 `emergency-repair force_enable`（200，cmb 5 行 +
node_probe + URSM 刷新）后归属坐实：glm-5.3（magpie 独占）E2E 200
`ROUND8-ATTRIB3-OK`，**节点账本 03:08:29 两条 200（GLM-5.3 E2E + trial probe）
callerKey 均为 `211-gateway`**。quota snapshot PATCH 200（来源 /v1/magpie/quotas）；
usage 200/200。

**脚本修复（本轮 2 处，magpie_gateway_register.py）**：
1. base_url 漂移轮里 existing 凭据**一律进 rotate 队列**（原先只看
   auth_failed，等不到——见上）；
2. rotate 成功后按凭据 `emergency-repair force_enable` 一次，把 cmb/URSM/
   node_probe 一并刷新（rotate 只改库，数据面不刷新就仍打旧 key）。
   SQL 手工收窄过的老凭据仍不要走这条路（force_enable 会把全部绑定置
   available，round-6-B 的告诫不变）。幂等复跑（无漂移）实测不触发新分支，
   REGISTER_RESULT=ok、E2E 200/2.0s。

**Maestro（7 flow 全绿，reports/20261107-032648，EXIT=0）**：原 5 flow
（m1 供应商 / m2 用量 / m3 网关 / g1 管理端列表 / g2 详情页）+ **新增 2 flow
覆盖 48h 窗口内上游合并的新功能**：
- **m4-routing-groups**（80ae904c2，路由组独立页）：侧栏导航项「路由组」、
  工具栏「新建组」（零分组页也渲染）、组编辑器开合（取消，不落保存）；
- **m5-key-limit-models**（1f439ecc5，额度按模型计数）：211-gateway 行
  「限额」→ 额度编辑器「模型」字段（默认「全部模型」）→ 菜单头
  「此额度计入的模型」+ `codex/*`/`<provider>/<model>` 条目。
  m5 的坑：行首还有**密钥本体**的模型选择 pill（旧功能 #905，文案同为
  「全部模型」、菜单头「此密钥可用的模型」）——Maestro `index: 1`（DOM 靠后）
  才命中额度编辑器的那颗。
- 附带 API 证据（361da10ee，页面 API 只答自己 origin）：POST
  `/api/usage/arrange` 同源 200；`Origin: http://127.0.0.1:9999` 与
  `https://evil.example` → 403，日志 `turned away … from another page`；
  `Origin: null` 的 GET → 403（#2242 的守卫仍在）。GET 跨源 200 是有意的
  （守卫只拦写）。

**未试**：trial 桶（Start Plan 周末桶）真实出活（本轮 E2E 用 glm-5.3/
glm-5.3-flash，trial 余额在 m2 卡片可见：114M/500M）；Windows/Linux 重放
（本轮无相关变更）。round8-node 保持运行（web :3446 / 网关 :3427），复跑：

```bash
# 节点（XDG 沙箱）
XDG_CONFIG_HOME=$PWD/.deployment-validation/round8-node/config \
XDG_CACHE_HOME=$PWD/.deployment-validation/round8-node/cache \
MAGPIE_WEB_KEY=$(cat .deployment-validation/round8-node/webkey.txt) \
  ./magpie web --addr 0.0.0.0:3446 --no-open
# 注册/E2E（密钥见 .deployment-validation/round8-node/{gateway-key-211-gateway,testkey}.txt）
MAGPIE_REGISTER_HOSTNAME=niuerdeMacBook-Pro.local MAGPIE_REGISTER_IP=192.168.31.36 \
  python3 scripts/gateway-register/magpie_gateway_register.py all --magpie http://127.0.0.1:3427
# Maestro
bash tests/maestro/run-local.sh
```
