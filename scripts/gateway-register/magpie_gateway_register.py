#!/usr/bin/env python3
# magpie_gateway_register.py — magpie 自动注册为 LLM Gateway 供应商（llm.itestu.cn / 211）+ 模型/余量/用量同步
#
# 纯 Python 标准库，跨平台（macOS / Linux / Windows）。
#
# 子命令:
#   identify                    检测本机身份（平台/主机名/LAN IP），打印 JSON
#   models                      读 magpie /v1/models，筛出独有模型，对照网关绑定
#   register                    幂等注册: provider(code=magpie-<host>) + credential + 模型绑定
#                               （已存在→409→PATCH 收敛 base_url/notes；IP 漂移自愈）
#   quota                       读 magpie quotas.json 余量 → PATCH 到网关 provider notes（同步余量）
#   usage                       读网关 /api/usage/providers/{id}（网关侧用量，请求后自动记账）
#   e2e                         以第三方身份（caller key）经网关 /v1/chat/completions 调 magpie 模型
#   all                         register → wait-probe → e2e → quota → usage 一条龙
#
# 环境变量: LLM_GATEWAY_SECRET（admin JWT 签名密钥）、LLM_GATEWAY_TEST_KEY（数据面 caller key）、
#           MAGPIE_CALLER_KEY（magpie 本地 key）、MAGPIE_CONFIG_DIR（magpie 配置目录，quota 用）
#
# 注册约定（2026-11-05）: code=magpie-<主机名小写去点>，(tenant_id, code) 唯一 → 重复注册 409，
# notes 带 {source, hostname, ip, platform, magpie_port, registered_at, registration:"auto"}。
# base_url 必须是 RFC1918 LAN IP（netbird CGNAT 段 100.64/10 会被网关 egress 守卫熔断）。

import argparse
import base64
import hashlib
import hmac
import json
import os
import platform
import socket
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_MODELS = ["zcode/GLM-5.3-Flash", "zcode/GLM-5.3", "zcode/GLM-5.2",
                  "zcode/GLM-5-Turbo", "zcode/GLM-5.3-Flash-Trial"]
DEFAULT_CONTEXT_WINDOW = 128000
RECORD = []  # 测试证据行
#2026-11-06 桶语义：trial（Start Plan 夜间桶）上游并发上限 3，与常规 serve
#池不同，拆成独立凭据让网关按凭据限流——trial 满时 STD 不被连坐排队。
#（trial 绑定的标准化名由 bind_models 按 raw 名小写取尾段得出。）
TRIAL_RAW = "zcode/GLM-5.3-Flash-Trial"
TRIAL_CONCURRENCY = 3
SECRETS = []  # 运行期收集的口令；log/evidence 输出前替换掉，key 不落终端/报告


def scrub(msg):
    for s in SECRETS:
        if s:
            msg = msg.replace(s, "[REDACTED]")
    return msg


def log(msg):
    print(scrub(msg), flush=True)


def evidence(key, value):
    line = "VERIFY_%s=%s" % (key, value)
    RECORD.append(scrub(line))
    log("[evidence] " + line)


# ── 身份检测（跨平台）───────────────────────────────────────────────
def detect_identity(gateway_url="https://llm.itestu.cn"):
    node = platform.node() or os.environ.get("COMPUTERNAME") or "unknown-host"
    host_override = os.environ.get("MAGPIE_REGISTER_HOSTNAME")  # 多平台测试时允许覆盖
    host = host_override or node
    code = "magpie-" + host.lower().replace(".", "")[:40]
    ip = os.environ.get("MAGPIE_REGISTER_IP")
    if not ip:
        u = urllib.parse.urlparse(gateway_url if "//" in gateway_url else "//" + gateway_url, scheme="https")
        # UDP connect 只做路由探测，端口不参与结果；按网关 scheme 取对应端口
        port = 443 if u.scheme == "https" else 8781
        ip = lan_ip(u.hostname or gateway_url, port) or "127.0.0.1"
    return {
        "platform": "%s/%s" % (platform.system(), platform.machine()),
        "python": platform.python_version(),
        "hostname": host,
        "fqdn": socket.getfqdn(),
        "code": code,
        "ip": ip,
    }


def _is_lan_ip(ip):
    """注册约定（2026-11-05）：base_url 必须是 RFC1918 LAN IP。回环、链路本地
    169.254/16、CGNAT 100.64/10（netbird，网关 egress 守卫会熔断）、fake-ip TUN
    198.18/15（clash verge 这类代理软件的 TUN 常占着它，UDP connect 的出口地址
    会变成 198.18.0.1）都不是，检测到它们等于没检测到。"""
    a = str(ip or "").split(".")
    if len(a) != 4 or not all(x.isdigit() and 0 <= int(x) <= 255 for x in a):
        return False
    n1, n2 = int(a[0]), int(a[1])
    return n1 == 10 or (n1 == 172 and 16 <= n2 <= 31) or (n1 == 192 and n2 == 168)


def lan_ip(host, port):
    """UDP connect 技巧取出口 IP；不通则回落本机地址枚举。跨平台。"""
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(3)
        s.connect((host, port))
        ip = s.getsockname()[0]
        s.close()
        if _is_lan_ip(ip):
            return ip
    except OSError:
        pass
    try:
        hostname = socket.gethostname()
        for info in socket.getaddrinfo(hostname, None, socket.AF_INET):
            ip = info[4][0]
            if _is_lan_ip(ip):
                return ip
    except OSError:
        pass
    return None


# ── HTTP ──────────────────────────────────────────────────────────
def http_json(method, url, body=None, headers=None, timeout=30):
    data = json.dumps(body).encode() if body is not None else None
    hdrs = {"Content-Type": "application/json"}
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, method=method, data=data, headers=hdrs)
    # clash verge 这类系统代理：shell 里有 http_proxy 时 urllib 会把 127.0.0.1 的
    # 请求也交给代理，而代理回不到本机回环 → 502/超时（本机 magpie :3426 打不通
    # 的典型原因）。回环目标一律直连。
    host = (urllib.parse.urlparse(url).hostname or "")
    opener = None
    if host in ("localhost", "::1") or host.startswith("127."):
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        r = (opener.open(req, timeout=timeout) if opener
             else urllib.request.urlopen(req, timeout=timeout))
        with r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw.strip() else {})
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except (ValueError, json.JSONDecodeError):
            return e.code, {"_raw": raw[:300]}
    except (urllib.error.URLError, OSError, socket.timeout) as e:
        return 0, {"_error": str(e)}


def admin_jwt(secret, ttl=900):
    """自签 super_admin JWT（HS256, claims 见网关 admin/auth.go）。"""
    if not secret:
        raise SystemExit("缺少网关 admin JWT secret（--secret 或 LLM_GATEWAY_SECRET）")
    claims = {"user_id": int(os.environ.get("LLM_GATEWAY_ADMIN_USER_ID", "41")),
              "tenant_id": os.environ.get("LLM_GATEWAY_TENANT_ID", "default"),
              "username": os.environ.get("LLM_GATEWAY_ADMIN_USERNAME", "admin"),
              "role": "super_admin",
              "must_change_password": False,
              "iss": "llm-gateway", "aud": ["llm-gateway-api"],
              "exp": int(time.time()) + ttl, "iat": int(time.time())}
    b64 = lambda d: base64.urlsafe_b64encode(d).rstrip(b"=")
    h = b64(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    p = b64(json.dumps(claims).encode())
    sig = b64(hmac.new(secret.encode(), h + b"." + p, hashlib.sha256).digest())
    return (h + b"." + p + b"." + sig).decode()


class Gateway:
    def __init__(self, base, secret=None, admin_user=None, admin_password=None):
        self.base = base.rstrip("/")
        if secret:
            self.jwt = admin_jwt(secret)
        elif admin_user and admin_password:
            self.jwt = self._login_jwt(admin_user, admin_password)
        else:
            raise SystemExit("网关鉴权缺失：--secret（自签 super_admin JWT）或 "
                             "--gateway-admin-user/--gateway-admin-password（登录换取）二者其一")
        self.h = {"Authorization": "Bearer " + self.jwt}

    def _login_jwt(self, user, password):
        """管理端登录换 token（admin/handler.go 的 /api/auth/token），与 UI/maestro 同路。"""
        st, body = http_json("POST", self.base + "/api/auth/token",
                             {"username": user, "password": password})
        if st != 200 or not isinstance(body, dict):
            raise SystemExit("网关登录失败（HTTP %s）：%s" % (st, str(body)[:160]))
        tok = (body.get("access_token") or body.get("token")
               or (body.get("data") or {}).get("access_token") or (body.get("data") or {}).get("token"))
        if not tok:
            raise SystemExit("网关登录响应里没有 token（HTTP %s）" % st)
        return tok

    def api(self, method, path, body=None, timeout=30):
        return http_json(method, self.base + path, body, self.h, timeout)

    def providers(self):
        st, body = self.api("GET", "/api/providers")
        # 非 200 时按空列表继续会误判 provider 不存在、进而误 POST，这里至少说一声
        if st != 200:
            log("GET /api/providers -> HTTP %s（鉴权或网关问题；下面按空列表处理）" % st)
        rows = body if isinstance(body, list) else body.get("providers") or body.get("data") or []
        return st, rows

    def find_by_code(self, code):
        _, rows = self.providers()
        for r in rows:
            if r.get("code") == code:
                return r
        return None

    def credentials(self, pid):
        st, body = self.api("GET", "/api/providers/%s/credentials" % pid)
        rows = body if isinstance(body, list) else body.get("credentials") or body.get("data") or []
        return rows

    def bindings(self, pid, cid):
        st, body = self.api("GET", "/api/providers/%s/credentials/%s/models" % (pid, cid))
        rows = body if isinstance(body, list) else body.get("models") or body.get("data") or []
        return rows


# ── magpie 侧 ─────────────────────────────────────────────────────
def magpie_models(magpie_base, caller_key, timeout=20):
    st, body = http_json("GET", magpie_base.rstrip("/") + "/v1/models",
                         headers={"Authorization": "Bearer " + caller_key}, timeout=timeout)
    if st != 200:
        return st, []
    return st, [m.get("id") for m in body.get("data", []) if m.get("id")]


def read_quotas(config_dir):
    """读 magpie 配置目录 quotas.json → 每账号余量摘要（多平台路径由调用方传入）。"""
    path = os.path.join(config_dir, "quotas.json")
    if not os.path.isfile(path):
        return None
    try:
        with open(path, encoding="utf-8") as f:
            raw = json.load(f)
    except (OSError, ValueError):
        return None
    out = {}
    now = time.time()
    for account, entry in raw.items():
        windows = (entry.get("quota") or {}).get("windows") or []
        wsum = []
        for w in windows:
            item = {"name": w.get("name"), "used": w.get("used")}
            if w.get("limit"):
                item["limit"] = w.get("limit")
                item["left"] = round(w["limit"] - (w.get("amount") if w.get("amount") is not None else (w.get("used") or 0) * (w["limit"] / 100.0)), 1)
            if w.get("resetsAt"):
                item["resetsAt"] = w.get("resetsAt")
            wsum.append(item)
        out[account] = {"windows": wsum, "readAt": entry.get("at")}
    return out


# ── register ──────────────────────────────────────────────────────
def cmd_register(args):
    ident = detect_identity(args.gateway)
    evidence("PLATFORM", ident["platform"])
    evidence("HOSTNAME", ident["hostname"])
    evidence("LAN_IP", ident["ip"])
    evidence("CODE", ident["code"])
    log("identity: %s" % json.dumps(ident, ensure_ascii=False))

    # 0) magpie 本地模型列表（自动检索）
    mst, models = magpie_models(args.magpie, args.magpie_caller_key)
    evidence("MAGPIE_MODELS_HTTP", mst)
    evidence("MAGPIE_MODELS_COUNT", len(models))
    unique = [m for m in models if m.startswith("zcode/")]
    if args.models is not None:
        unique = [m for m in args.models.split(",") if m]
    want = unique or ([] if not models else DEFAULT_MODELS)
    if not want:
        log("magpie serves no models (HTTP %s, %d ids) — registering with no bindings" % (mst, len(models)))
    log("magpie models: %d total, bind candidates: %s" % (len(models), want))
    evidence("BIND_MODELS", ",".join(want) or "none")

    # base_url 守卫：自动检测到的地址必须是 RFC1918 LAN IP（注册约定 2026-11-05）。
    # 回环、CGNAT、fake-ip TUN（198.18.0.1，clash verge 实测会这样）拿去当 base_url
    # 都会让网关回源打到假地址——幂等路径更会把一个好好的 base_url PATCH 坏（本轮
    # 就把 95436 的 base_url PATCH 成过 http://198.18.0.1:3426/v1，2026-11-07 修复）。
    # 真有特殊地址用 MAGPIE_REGISTER_IP 显式声明（显式值是操作者自己的决定，不拦）。
    if not os.environ.get("MAGPIE_REGISTER_IP") and not _is_lan_ip(ident["ip"]):
        log("检测到的地址 %s 不是 RFC1918 LAN IP——拒绝以它注册/收敛 base_url"
            "（网关回源会打到假地址）。用 MAGPIE_REGISTER_IP 显式指定本机对网关可达的地址后重跑。" % ident["ip"])
        evidence("REGISTER_RESULT", "fail-non-lan-ip")
        return 1

    gw = Gateway(args.gateway, args.secret, args.gateway_admin_user, args.gateway_admin_password)

    # 1) 幂等注册 provider
    code = args.code or ident["code"]
    base_url = "http://%s:%s/v1" % (ident["ip"], urlparse_port(args.magpie))
    notes = json.dumps({
        "source": "magpie", "hostname": ident["fqdn"], "host": ident["hostname"],
        "ip": ident["ip"], "platform": ident["platform"], "magpie_port": urlparse_port(args.magpie),
        "registered_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "registration": "auto",
        "magpie_version_hint": "serve-instance",
    }, ensure_ascii=False)
    payload = {"code": code, "display_name": "magpie @ %s (%s)" % (ident["hostname"], ident["ip"]),
               "base_url": base_url, "protocol": "openai-completions",
               "catalog_code": "__custom__", "notes": notes}
    existing = gw.find_by_code(code)
    if existing:
        pid = existing["id"]
        log("provider exists: id=%s code=%s (幂等: 重复注册收敛为 PATCH)" % (pid, code))
        evidence("REGISTER_MODE", "idempotent-patch")
        # display_name 里嵌了注册时的 IP，漂移后一起收敛
        patch = {"notes": notes}
        if (existing.get("display_name") or "") != payload["display_name"]:
            patch["display_name"] = payload["display_name"]
        cur_url = existing.get("base_url") or ""
        if cur_url != base_url:
            patch["base_url"] = base_url
            st, body = gw.api("PATCH", "/api/providers/%s" % pid, patch)
            log("base_url drift %s → %s : PATCH %s" % (cur_url, base_url, st))
        else:
            log("base_url 已一致 (%s)，刷新 notes/display_name" % base_url)
            st, body = gw.api("PATCH", "/api/providers/%s" % pid, patch)
        evidence("PROVIDER_ID", pid)
    else:
        st, body = gw.api("POST", "/api/providers", payload)
        log("create provider: HTTP %s %s" % (st, json.dumps(body, ensure_ascii=False)[:160]))
        if st not in (200, 201):
            evidence("REGISTER_RESULT", "fail-http%s" % st)
            return 1
        pid = body.get("id") or body.get("provider", {}).get("id")
        evidence("REGISTER_MODE", "create")
        evidence("PROVIDER_ID", pid)

    #2) credentials 按桶拆分：serve（常规 GLM） plus trial（Start Plan桶，并发 3）
    creds = gw.credentials(pid)
    trial_models = [m for m in want if m == TRIAL_RAW]
    std_models = [m for m in want if m != TRIAL_RAW]

    def ensure_cred(label, conc):
        cred = next((c for c in creds if (c.get("label") or "") == label), None)
        if cred:
            cid = cred["id"]
            log("credential exists: id=%s label=%s" % (cid, label))
            evidence("CREDENTIAL_MODE_" + label.upper().replace("-", "_"), "existing")
        else:
            st, body = gw.api("POST", "/api/providers/%s/credentials" % pid,
                    {"label": label, "api_key": args.magpie_caller_key,
                    "plan_type": "free", "concurrency_limit": conc, "fp_slot_limit": conc})
            log("create credential %s: HTTP %s %s" % (label, st, json.dumps(body, ensure_ascii=False)[:120]))
            if st not in (200,201):
                    evidence("REGISTER_RESULT", "fail-cred-http%s" % st)
                    return None
            cid = body.get("id") or (body.get("credential") or {}).get("id")
            evidence("CREDENTIAL_MODE_" + label.upper().replace("-", "_"), "create")
        return cid

    cid = ensure_cred(args.cred_label, args.serve_concurrency)
    trial_cid = ensure_cred("magpie-trial", TRIAL_CONCURRENCY) if trial_models else None
    if cid is None and trial_cid is None:
        return 1
    # 有凭据创建失败时继续把能绑的绑完（work 尽量做），但结论记 partial、退出非 0，
    # 不再让末尾的 REGISTER_RESULT=ok 把 fail-cred 证据行盖掉
    cred_failed = cid is None or (trial_models and trial_cid is None)
    if cid is not None:
        evidence("CREDENTIAL_ID", cid)
    if trial_cid is not None:
        evidence("TRIAL_CREDENTIAL_ID", trial_cid)

    #3) 模型绑定（显式带 context_window 规避旧版 nil ContextWindow42P08；
    #500 时提示走 bind-via-ssh.sh SQL fallback）
    def bind_models(target_cid, models):
        if target_cid is None or not models:
            return
        bound = gw.bindings(pid, target_cid)
        bound_std = {b.get("standardized_name") or b.get("raw_model_name") for b in bound}
        log("existing bindings on %s: %s" % (target_cid, sorted(x for x in bound_std if x)))
        for m in models:
            std = m.split("/")[-1].lower()
            if std in bound_std:
                log("binding exists: %s to %s" % (std, m))
                continue
            st, body = gw.api("POST", "/api/providers/%s/credentials/%s/models" % (pid, target_cid),
                    {"raw_model_name": m, "standardized_name": std,
                    "outbound_model_name": m, "available": True,
                    "context_window": args.context_window})
            log("bind %s to %s : HTTP %s %s" % (std, m, st, json.dumps(body, ensure_ascii=False)[:120]))

    bind_models(cid, std_models)
    bind_models(trial_cid, trial_models)
    all_live = set()
    for target in (cid, trial_cid):
        if target is not None:
            all_live |= {b.get("standardized_name") or b.get("raw_model_name") for b in gw.bindings(pid, target)}
    evidence("BINDINGS_AFTER", ",".join(sorted(x for x in all_live if x)))

    if cred_failed:
        log("部分凭据创建失败（见上方 fail-cred 证据行）——REGISTER_RESULT 记 partial")
        evidence("REGISTER_RESULT", "partial")
        return 1
    evidence("REGISTER_RESULT", "ok")
    return 0

def urlparse_port(magpie_url):
    p = urllib.parse.urlparse(magpie_url)
    return str(p.port or (443 if p.scheme == "https" else 80))


# ── wait-probe / e2e / quota / usage ──────────────────────────────
def cmd_e2e(args):
    """第三方视角：只用 caller key 经网关访问（不带 admin JWT）。"""
    model = args.model or "glm-5.3-flash"
    body = {"model": model, "messages": [{"role": "user",
            "content": args.prompt or "Reply with exactly: GATEWAY-E2E-OK and nothing else."}],
            "max_tokens": args.max_tokens or 400}
    t0 = time.time()
    st, resp = http_json("POST", args.gateway.rstrip("/") + "/v1/chat/completions", body,
                         {"Authorization": "Bearer " + args.test_key}, timeout=120)
    dt = time.time() - t0
    if st == 200 and resp.get("choices"):
        content = resp["choices"][0]["message"].get("content")
        log("E2E HTTP 200 in %.1fs model=%s upstream=%s" % (dt, model, resp.get("model")))
        log("content: %s" % json.dumps(content, ensure_ascii=False)[:200])
        log("usage: %s" % json.dumps(resp.get("usage"), ensure_ascii=False))
        evidence("E2E_STATUS", "200")
        evidence("E2E_SECONDS", "%.1f" % dt)
        return 0
    log("E2E FAIL HTTP %s: %s" % (st, json.dumps(resp, ensure_ascii=False)[:400]))
    evidence("E2E_STATUS", str(st) or "err")
    return 1


def cmd_quota(args):
    """余量同步: magpie quotas.json → PATCH provider notes.quota_snapshot。"""
    ident = detect_identity(args.gateway)
    q = read_quotas(args.magpie_config_dir)
    if q is None:
        log("quotas.json 不存在于 %s（用 --magpie-config-dir 指定 magpie 配置目录）" % args.magpie_config_dir)
        evidence("QUOTA_SYNC", "missing-config")
        return 1
    snap = {"quota_synced_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "accounts": {k: v for k, v in list(q.items()) if "zcode" in k.lower()} or q}
    log("quota accounts: %d (zcode: %d)" % (len(q), len(snap["accounts"])))
    for acc, v in snap["accounts"].items():
        for w in v["windows"]:
            if w.get("limit"):
                log("  %s | %s: %s" % (acc, w["name"], w.get("display", "%s / %s" % (w.get("used"), w["limit"]))))
    gw = Gateway(args.gateway, args.secret, args.gateway_admin_user, args.gateway_admin_password)
    code = args.code or ident["code"]
    p = gw.find_by_code(code) or gw.find_by_code("magpie-zcode")
    if not p:
        log("provider 未注册（先跑 register）")
        evidence("QUOTA_SYNC", "no-provider")
        return 1
    notes = p.get("notes") or ""
    try:
        note_obj = json.loads(notes) if notes.strip().startswith("{") else {"legacy": notes}
    except ValueError:
        note_obj = {"legacy": notes}
    note_obj["quota_snapshot"] = snap
    st, _ = gw.api("PATCH", "/api/providers/%s" % p["id"], {"notes": json.dumps(note_obj, ensure_ascii=False)})
    log("quota snapshot → provider %s notes: HTTP %s" % (p["id"], st))
    evidence("QUOTA_SYNC", "patch-%s" % st)
    return 0 if st == 200 else 1


def cmd_usage(args):
    """网关侧用量（请求经网关即自动记账）。"""
    gw = Gateway(args.gateway, args.secret, args.gateway_admin_user, args.gateway_admin_password)
    ident = detect_identity(args.gateway)
    code = args.code or ident["code"]
    p = gw.find_by_code(code)
    if not p:
        log("provider 未注册")
        return 1
    pid = p["id"]
    st, s = gw.api("GET", "/api/usage/providers/%s" % pid)
    log("usage summary(provider %s): HTTP %s\n%s" % (pid, st, json.dumps(s, ensure_ascii=False)[:600]))
    st2, m = gw.api("GET", "/api/usage/providers/%s/models" % pid)
    log("usage by models: HTTP %s\n%s" % (st2, json.dumps(m, ensure_ascii=False)[:600]))
    evidence("USAGE_READ", "%s/%s" % (st, st2))
    return 0 if st == 200 else 1


def cmd_models(args):
    mst, models = magpie_models(args.magpie, args.magpie_caller_key)
    log("magpie HTTP %s, %d models" % (mst, len(models)))
    gw = Gateway(args.gateway, args.secret, args.gateway_admin_user, args.gateway_admin_password)
    ident = detect_identity(args.gateway)
    p = gw.find_by_code(args.code or ident["code"])
    if p:
        for c in gw.credentials(p["id"]):
            bs = gw.bindings(p["id"], c["id"])
            log("credential %s (%s): %d bindings: %s" % (
                c["id"], c.get("label"), len(bs),
                sorted((b.get("standardized_name") or b.get("raw_model_name")) for b in bs)))
    zc = [m for m in models if m.startswith("zcode/")]
    log("magpie 独有模型 (%d): %s" % (len(zc), zc))
    return 0


def cmd_all(args):
    rc = cmd_register(args)
    if rc:
        return rc
    log("\n── 等待探针收敛 (probe) ──")
    time.sleep(args.probe_wait)
    rc = cmd_e2e(args)
    if args.magpie_config_dir:
        cmd_quota(args)
    cmd_usage(args)
    return rc


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("cmd", choices=["identify", "models", "register", "e2e", "quota", "usage", "all"])
    ap.add_argument("--gateway", default="https://llm.itestu.cn")
    ap.add_argument("--magpie", default="http://127.0.0.1:3426")
    ap.add_argument("--secret", default=os.environ.get("LLM_GATEWAY_SECRET"))
    ap.add_argument("--gateway-admin-user", default=os.environ.get("LLM_GATEWAY_ADMIN_USER"),
                    help="管理端登录账号（与 --gateway-admin-password 二选一路径：登录换 token，不必持有签名密钥）")
    ap.add_argument("--gateway-admin-password", default=os.environ.get("LLM_GATEWAY_ADMIN_PASSWORD"))
    ap.add_argument("--test-key", default=os.environ.get("LLM_GATEWAY_TEST_KEY"))
    ap.add_argument("--magpie-caller-key", default=os.environ.get("MAGPIE_CALLER_KEY"))
    ap.add_argument("--magpie-config-dir", default=os.environ.get("MAGPIE_CONFIG_DIR"))
    ap.add_argument("--code", help="覆盖 provider code（默认 magpie-<主机名>）")
    ap.add_argument("--models", help="逗号分隔要绑定的 magpie 模型 id（默认自动检索 zcode/*）")
    ap.add_argument("--model", help="e2e 用的网关公开模型名")
    ap.add_argument("--prompt")
    ap.add_argument("--max-tokens", type=int)
    ap.add_argument("--context-window", type=int, default=DEFAULT_CONTEXT_WINDOW)
    ap.add_argument("--cred-label", default="magpie-serve")
    ap.add_argument("--serve-concurrency", type=int, default=3,
                       help="serve credential concurrency (trial 固定 3)")
    ap.add_argument("--probe-wait", type=int, default=20)
    args = ap.parse_args()

    SECRETS.extend(v for v in (args.secret, args.test_key, args.magpie_caller_key,
                               args.gateway_admin_password) if v)

    if args.cmd == "identify":
        log(json.dumps(detect_identity(args.gateway), ensure_ascii=False))
        return 0
    if not args.magpie_caller_key and args.cmd in ("register", "models", "all"):
        raise SystemExit("缺少 --magpie-caller-key / MAGPIE_CALLER_KEY")
    has_auth = args.secret or (args.gateway_admin_user and args.gateway_admin_password)
    if not has_auth and args.cmd in ("register", "quota", "usage", "models", "all"):
        raise SystemExit("缺少网关鉴权：--secret / LLM_GATEWAY_SECRET，或 "
                         "--gateway-admin-user + --gateway-admin-password（LLM_GATEWAY_ADMIN_USER/PASSWORD）")
    if not args.test_key and args.cmd in ("e2e", "all"):
        raise SystemExit("缺少 --test-key / LLM_GATEWAY_TEST_KEY")
    fn = {"models": cmd_models, "register": cmd_register, "e2e": cmd_e2e,
          "quota": cmd_quota, "usage": cmd_usage, "all": cmd_all}[args.cmd]
    return fn(args)


if __name__ == "__main__":
    sys.exit(main() or 0)
