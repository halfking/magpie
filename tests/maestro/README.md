# tests/maestro — 自动注册链路的 Maestro WEB 黑盒测试

用 [Maestro](https://maestro.mobile.dev)（2.11.0，WEB 平台 = CDP Chrome）以真浏览器
驱动两处 UI，黑盒验证「magpie 自动注册为 llm.itestu.cn 供应商」整条功能：

| flow | 端 | 验什么 |
| --- | --- | --- |
| m1-magpie-providers | magpie web | 本机 agent 登录态被发现：供应商页出现已登录 ZCode、GLM Coding Max |
| m2-magpie-usage | magpie web | start plan 窗口卡片（5 小时/每周）与剩余额度走势 —— quota 同步的数据源 |
| m3-magpie-gateway | magpie web | 网关运行、共享密钥、独有模型 zcode/GLM-5.3 —— 注册的出口侧 |
| m4-routing-groups | magpie web | 48h 新功能（80ae904c2）：「路由组」独立页——导航项、工具栏「新建组」、组编辑器开合 |
| m5-key-limit-models | magpie web | 48h 新功能（1f439ecc5）：网关密钥额度的「此额度计入的模型」选择器与其模型菜单 |
| g1-gateway-registered | llm.itestu.cn 管理端 | 供应商列表出现自动注册的 `magpie @ <host>` 行 |
| g2-gateway-provider-detail | llm.itestu.cn 管理端 | 详情页：base_url 指向本机 magpie、`magpie-serve` 凭据、模型就位 |

脚本链路本身（register/e2e/quota/usage 的 API 证据）在
[scripts/gateway-register/TEST-REPORT.md](../../scripts/gateway-register/TEST-REPORT.md)；
本套件补的是 Maestro 管理的两端 UI 证明。

## 前置

1. **magpie 本地节点**（被测实例，任意端口）：

   ```bash
   MAGPIE_WEB_KEY=<16+字符key> magpie web --addr 0.0.0.0:3446 --no-open
   # settings.json 里 lan:true 才会把网关绑到 0.0.0.0（--lan 只作用 web 页）
   # settings.json 里 lang:"zh" 钉住 UI 语言，headless Chrome 英文环境下断言才稳
   # （合法值 system|en|zh|zh-TW|ja|de——写 "zh-CN" 会被校验拒掉，UI 落回英文）
   ```

   节点要有已登录的 ZCode（accounts discovery 默认开，magpie 自动读 ~/.zcode），
   且 `magpie gateway-key add 211-gateway` 建过共享密钥。

2. **自动注册**：跑
   [scripts/gateway-register/magpie_gateway_register.py](../../scripts/gateway-register/magpie_gateway_register.py)
   的 `all`（新 provider 首次注册后，网关要
   `PATCH /api/routing/emergency-repair` force_enable 才立即可路由——URSM 是进程
   启动快照，中途注册的 provider 不自动生效）。

3. **tests/maestro/.env.local**（gitignore，模板见 [env.yaml](env.yaml)）：
   MAGPIE_URL（带 `?k=<key>`）、GATEWAY_URL、GATEWAY_PROVIDER_ID、
   GATEWAY_MAGPIE_HOST（显示名里 @ 后面的主机名）、GATEWAY_MAGPIE_BASE
   （`host:port`，断言 base_url 用）、GATEWAY_ADMIN_USER/PASSWORD。

   管理端账号用独立测试账号，别用日常 admin：连续登录失败会触发验证码闸门
   （admin/auth_captcha.go 的 CaptchaFailureThreshold），触发后登录 flow 必然失败，
   要先清失败计数再跑。

## 跑

```bash
bash tests/maestro/run-local.sh          # 全量；产物在 reports/<时间戳>/
bash tests/maestro/run-local.sh m2-magpie-usage     # 只跑一个
```

run-local.sh 自起一个 `--headless=new` 的 CDP Chrome（:9222，zh-CN，独立
profile，退出时收掉），逐 flow `maestro test --driver-host-port 9222 -e K=V`，
最后落 summary.txt。已有一个活的 CDP Chrome 在该端口时直接复用、不动它。

## 已知事实（踩过的坑）

- Maestro CLI 2.11.0 **没有** `--env-file`，flow 的 `${VAR}` 也**不读** shell 环境
  变量——只能 `-e K=V` 逐个传（口令经进程 argv，本机 ps 可见，不落盘不进 git）。
- `.env.local` 被 run-local.sh **bash source** 读入：写 shell 语法 `KEY="value"`，
  别照 env.yaml 的 `KEY: "value"` YAML 形状抄（source 会报 command not found）。
- magpie web 的导航是 `data-view` 属性的单页切换，没有 URL 路由：flow 里用
  `tapOn: '供应商'` 导航，靠 DOM 顺序命中侧边导航（侧边栏在 main 之前）。
- llm.itestu.cn 登录框的**用户名默认预填 admin**，`inputText` 是**追加**——
  先 `eraseText`，否则得到 adminadmin、401。
- 登录提交用 `pressKey: Enter`，页面上「登录」文案出现多处，tapOn 会点错。
- 详情页默认页签没有 notes/registration 字段，注册方式的证明走 API
  （GET /api/providers 的 notes.registration=auto），UI 层断言 display_name
  与 base_url 即可。
- **Windows 上不要传 `--driver-host-port`**（run-local.sh 已默认不传，要旧行为
  `MAESTRO_PASS_DRIVER_PORT=1`）：maestro 对该端口做的是 ServerSocket 试绑探测，
  CDP Chrome 占着 127.0.0.1:9222 时，Windows 的 wildcard 绑定必然 EADDRINUSE
  （macOS 靠 SO_REUSEADDR 语义放行，Java 在 Windows 不开）。WEB 驱动的
  CdpClient 默认就连 localhost:9222，不传参数行为不变。
- 供应商卡的套餐名随账号而异（GLM Coding Max/Lite/Pro…），flow 只断言
  `GLM Coding.*` 前缀，别写死套餐。
