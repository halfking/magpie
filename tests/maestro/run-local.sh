#!/usr/bin/env bash
# tests/maestro/run-local.sh — magpie 自动注册链路的 Maestro WEB 实跑入口
#
# 形制抄自 llm-gateway-go 仓库的 maestro/run-local.sh（同一家网关的既有做法）：
# 自起一个 CDP Chrome（headless），把 .env.local 里的变量逐个 -e 传给 maestro，
# 逐 flow 跑并汇总。Maestro CLI 2.11.0 没有 --env-file，${VAR} 也不读 shell 环境。
#
# 用法：
#   bash tests/maestro/run-local.sh                # 全量（m1..m5 + g1..g2）
#   bash tests/maestro/run-local.sh m1-magpie-providers   # 只跑名字匹配的 flow
#
# 前置：
#   1. 一个跑起来的 magpie 本地节点（见 README.md 的启动命令）
#   2. tests/maestro/.env.local（MAGPIE_URL / GATEWAY_URL / GATEWAY_PROVIDER_ID /
#      GATEWAY_MAGPIE_HOST / GATEWAY_MAGPIE_BASE / GATEWAY_ADMIN_USER /
#      GATEWAY_ADMIN_PASSWORD；模板见 env.yaml）
#   3. Chrome（MAESTRO_CHROME_BIN 可指定）与 JDK 17（MAESTRO_JAVA_HOME）
#
# 产物：tests/maestro/reports/<时间戳>/<flow>.log 与截图、summary.txt
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${HERE}"

DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
REPORT_ROOT="${HERE}/reports/${RUN_STAMP:-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "${REPORT_ROOT}"

# ── 1. 选 flow ───────────────────────────────────────────────────────────────
FLOWS=()
for arg in "$@"; do
  FLOWS+=("flows/${arg}.yaml")
done
if [ "${#FLOWS[@]}" -eq 0 ]; then
  FLOWS=(
    flows/m1-magpie-providers.yaml
    flows/m2-magpie-usage.yaml
    flows/m3-magpie-gateway.yaml
    flows/m4-routing-groups.yaml
    flows/m5-key-limit-models.yaml
    flows/g1-gateway-registered.yaml
    flows/g2-gateway-provider-detail.yaml
  )
fi

# ── 2. JDK（maestro 是 Java 应用）────────────────────────────────────────────
find_jdk() {
  local c
  for c in "${MAESTRO_JAVA_HOME:-}" "${JAVA_HOME:-}"; do
    [ -n "${c}" ] && { [ -x "${c}/bin/java" ] || [ -x "${c}/bin/java.exe" ]; } && { printf '%s' "${c}"; return; }
  done
  if command -v /usr/libexec/java_home >/dev/null 2>&1; then
    c="$(/usr/libexec/java_home -v 17 2>/dev/null || true)"
    [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  fi
  for c in /opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
           /Library/Java/JavaVirtualMachines/*/Contents/Home \
           "/c/Program Files/Eclipse Adoptium/"jdk-* \
           "/c/Program Files/Java/"jdk-*; do
    [ -n "${c}" ] && { [ -x "${c}/bin/java" ] || [ -x "${c}/bin/java.exe" ]; } && { printf '%s' "${c}"; return; }
  done
  # PATH 上的 java：bin 的上级就是 JAVA_HOME
  local j
  j="$(command -v java 2>/dev/null || true)"
  if [ -n "${j}" ]; then
    c="$(cd "$(dirname "$(readlink -f "${j}" 2>/dev/null || printf '%s' "${j}")")/.." && pwd)"
    { [ -x "${c}/bin/java" ] || [ -x "${c}/bin/java.exe" ]; } && { printf '%s' "${c}"; return; }
  fi
  printf ''
}
JAVA_HOME_CANDIDATE="$(find_jdk)"
[ -n "${JAVA_HOME_CANDIDATE}" ] || { echo "!! 找不到 JDK 17（设 MAESTRO_JAVA_HOME）" >&2; exit 2; }
export JAVA_HOME="${JAVA_HOME_CANDIDATE}"
export PATH="${JAVA_HOME}/bin:${PATH}"
command -v maestro >/dev/null 2>&1 || PATH="$HOME/.maestro/bin:$PATH"   # Windows 默认安装位
command -v maestro >/dev/null 2>&1 || { echo "!! PATH 里没有 maestro" >&2; exit 2; }

# ── 3. 变量（口令只从 tests/maestro/.env.local 读，不进 git）────────────────
[ -f "${HERE}/.env.local" ] || { echo "!! 缺 ${HERE}/.env.local（模板见 env.yaml）" >&2; exit 2; }
set -a
# shellcheck disable=SC1091
. "${HERE}/.env.local"
set +a
: "${MAGPIE_URL:?缺 MAGPIE_URL}"
: "${GATEWAY_URL:?缺 GATEWAY_URL}"
: "${GATEWAY_PROVIDER_ID:?缺 GATEWAY_PROVIDER_ID}"
: "${GATEWAY_MAGPIE_HOST:?缺 GATEWAY_MAGPIE_HOST}"
: "${GATEWAY_MAGPIE_BASE:?缺 GATEWAY_MAGPIE_BASE}"
: "${GATEWAY_ADMIN_USER:?缺 GATEWAY_ADMIN_USER}"
: "${GATEWAY_ADMIN_PASSWORD:?缺 GATEWAY_ADMIN_PASSWORD}"

# ── 4. Chrome（CDP）──────────────────────────────────────────────────────────
find_chrome() {
  if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium" \
           "$(command -v google-chrome 2>/dev/null || true)" \
           "$(command -v chromium 2>/dev/null || true)" \
           "${LOCALAPPDATA:-}/Google/Chrome/Application/chrome.exe" \
           "/c/Program Files/Google/Chrome/Application/chrome.exe" \
           "/c/Program Files (x86)/Google/Chrome/Application/chrome.exe"; do
    [ -n "${c}" ] && [ -x "${c}" ] && { printf '%s' "${c}"; return; }
  done
  printf ''
}
CHROME_BIN="$(find_chrome)"
[ -n "${CHROME_BIN}" ] || { echo "!! 找不到 Chrome（设 MAESTRO_CHROME_BIN）" >&2; exit 2; }

# curl 一律 --noproxy '*'：clash verge 这类代理软件把 http_proxy 塞进 shell 时，
# 探测 127.0.0.1:9222 的请求会被交给代理而打不到本机 CDP（本地端口"无法访问"的
# 典型原因），CDP 探测必须直连。
CHROME_PROFILE="${MAESTRO_CHROME_PROFILE:-/tmp/maestro-magpie-chrome-profile}"
CHROME_PID=""
if ! curl -s --noproxy '*' --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
  echo "== 自起 Chrome（CDP :${DRIVER_PORT}，zh-CN）"
  mkdir -p "${CHROME_PROFILE}"
  nohup "${CHROME_BIN}" \
    --headless=new --remote-debugging-port="${DRIVER_PORT}" \
    --user-data-dir="${CHROME_PROFILE}" \
    --window-size="${MAESTRO_CHROME_WINDOW:-1280,2000}" \
    --lang=zh-CN --accept-lang=zh-CN \
    --no-first-run --no-default-browser-check --disable-gpu \
    about:blank >"${REPORT_ROOT}/.chrome.log" 2>&1 &
  CHROME_PID=$!
  for _ in $(seq 1 30); do
    curl -s --noproxy '*' --max-time 2 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -s --noproxy '*' --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1 \
    || { echo "!! Chrome 起不来（看 ${REPORT_ROOT}/.chrome.log）" >&2; exit 2; }
fi
# 脚本退出时收掉自己起的 Chrome——测试启动的东西随测试结束结束
cleanup() { [ -n "${CHROME_PID}" ] && kill "${CHROME_PID}" 2>/dev/null || true; }
trap cleanup EXIT

# ── 5. 逐 flow 跑 ────────────────────────────────────────────────────────────
E_ARGS=(
  -e "MAGPIE_URL=${MAGPIE_URL}"
  -e "GATEWAY_URL=${GATEWAY_URL}"
  -e "GATEWAY_PROVIDER_ID=${GATEWAY_PROVIDER_ID}"
  -e "GATEWAY_MAGPIE_HOST=${GATEWAY_MAGPIE_HOST}"
  -e "GATEWAY_MAGPIE_BASE=${GATEWAY_MAGPIE_BASE}"
  -e "GATEWAY_ADMIN_USER=${GATEWAY_ADMIN_USER}"
  -e "GATEWAY_ADMIN_PASSWORD=${GATEWAY_ADMIN_PASSWORD}"
)
echo "== 跑 ${#FLOWS[@]} 个 flow → ${REPORT_ROOT}"
FAILS=0
# --driver-host-port 会触发 maestro 的端口 bind 探测（ServerSocket 试绑）：Chrome
# 占着 127.0.0.1:<port> 时，Windows 上 wildcard 绑定必然 EADDRINUSE（macOS 靠
# SO_REUSEADDR 语义放行）。WEB 驱动的 CdpClient 默认就连 localhost:9222，所以
# Windows（或任何 bind 语义严格的平台）保持不传；要旧行为显式 MAESTRO_PASS_DRIVER_PORT=1。
PORT_ARGS=()
if [ "${MAESTRO_PASS_DRIVER_PORT:-0}" = "1" ]; then
  PORT_ARGS=(--driver-host-port "${DRIVER_PORT}")
fi
for flow in "${FLOWS[@]}"; do
  name="$(basename "${flow}" .yaml)"
  echo "---- ${name}"
  if maestro test \
      ${PORT_ARGS+"${PORT_ARGS[@]}"} \
      --headless \
      --no-ansi \
      --screen-size "${MAESTRO_SCREEN_SIZE:-1280x2000}" \
      --test-output-dir "${REPORT_ROOT}/${name}" \
      "${E_ARGS[@]}" \
      "${flow}" >"${REPORT_ROOT}/${name}.log" 2>&1; then
    echo "   PASS"
  else
    echo "   FAIL（日志 ${REPORT_ROOT}/${name}.log）"
    FAILS=$((FAILS + 1))
  fi
done

# 汇总
{
  echo "flows: ${#FLOWS[@]}  failed: ${FAILS}"
  grep -l "FAIL" "${REPORT_ROOT}"/*.log 2>/dev/null || true
} >"${REPORT_ROOT}/summary.txt"
cat "${REPORT_ROOT}/summary.txt"
[ "${FAILS}" -eq 0 ]
