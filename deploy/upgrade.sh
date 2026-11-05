#!/usr/bin/env bash
# =============================================================================
# AegisOps 四期发布/升级驱动器（配合 deploy/install.sh，不替代它）
#
# 一句话：把「取码 → 算不可变 TAG → 调 install.sh 发布 → 验收 → 记账」串成一条命令；
# 装坏了 --rollback 一条命令回到上一个好版本。
#
# 为什么不再写一个安装脚本：install.sh 本身就是幂等调谐器，「升级」= 换个 TAG 重跑一遍。
# 本脚本只做 install.sh 刻意不管的四件事：源码目录常驻、TAG 与 commit 绑定、发布后验收、
# 发布记录与回滚。它不 apply 任何清单、不碰 Secret、不自己 rollout —— 全部委托 install.sh，
# 避免两套发布逻辑各自漂移（红线由 tests/arch/upgrade_script_test.go 守住）。
#
# 口径唯一来源：docs/03-运维与部署/01-部署与安装.md §4.11。改行为先改文档。
# =============================================================================
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
INSTALL_SH="${SCRIPT_DIR}/install.sh"
RUN_TS="$(date +%Y%m%d-%H%M%S)"

# 调用方已经导出的变量名快照：必须在下面赋默认值之前抓，之后用来判断
# 「PUSH/BUILD 到底是谁定的」。upgrade.sh 只在调用方没表态时才替它决定。
ENV_VAR_NAMES=" $(compgen -e | tr '\n' ' ') "
key_from_env() { [[ "$ENV_VAR_NAMES" == *" $1 "* ]]; }

# --- 可配置项 ---------------------------------------------------------------
: "${AEGISOPS_DEPLOY_STATE:=${HOME}/.aegisops/deploy}"
SRC_DIR=""            # 常驻源码目录，默认 upgrade.sh 所在的这份仓库
STATE_DIR="${AEGISOPS_DEPLOY_STATE}"
ENV_FILE=""
GIT_REMOTE="origin"   # 取码用的 remote 名；SRC_DIR 不存在时按它的 URL 克隆
GIT_REMOTE_URL=""     # 直接给地址（首次 bootstrap，SRC_DIR 还不存在时用）
REF=""                # 要发布的分支/tag/commit；给了就会 fetch + detach checkout
NO_FETCH=0
ALLOW_DIRTY=0
FOLLOW_ENV_TAG=0
NO_BUILD=0
ROLLBACK=0
DO_STATUS=0
DO_DIFF=0
DRY_RUN=0
ASSUME_YES=0
VERIFY_TIMEOUT=300
# 导出的 TAG 视为显式指定（沿用 install.sh「导出的环境变量 > env 文件」的优先级），
# 不给才由 compute_tag 按 git 短 sha 派生；显式指定一律不加 commit 后缀。
: "${TAG:=}"
EXTRA_ARGS=()         # -- 之后的参数原样透给 install.sh
: "${KUBECTL_BIN:=kubectl}"

# 运行期
NAMESPACE=""
FORM=""
IMAGE_BASE=""
WEB_IMAGE_BASE=""
TARGET_SHA=""
TARGET_REF=""
TARGET_TAG=""
STATE_FILE=""
HISTORY_FILE=""
LOG_DIR=""
UPGRADE_LOG=""
DEPLOYED_COMMIT=""

if [[ -t 1 ]]; then
  C_DIM=$'\033[2m'; C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'; C_BLU=$'\033[36m'; C_OFF=$'\033[0m'
else
  C_DIM=""; C_RED=""; C_GRN=""; C_YLW=""; C_BLU=""; C_OFF=""
fi

_emit() { printf '%s\n' "$*"; }
info() { _emit "${C_BLU}[信息]${C_OFF} $*"; }
ok() { _emit "${C_GRN}[ ok ]${C_OFF} $*"; }
warn() { _emit "${C_YLW}[警告]${C_OFF} $*" >&2; }

die() {
  local cause="$1" next="${2:-}"
  _emit "" >&2
  _emit "${C_RED}发布失败${C_OFF}" >&2
  _emit "  根因  ：${cause}" >&2
  if [[ -n "$next" ]]; then _emit "  下一步：${next}" >&2; fi
  _emit "  日志  ：${UPGRADE_LOG:-（尚未建立）}" >&2
  _emit "" >&2
  exit 1
}

usage() {
  cat <<'EOF'
aegisops 一键发布/升级（deploy/upgrade.sh）

它不重复实现安装：真正的发布动作始终由 deploy/install.sh 的九阶段完成，
本脚本负责取码、镜像 TAG、发布后验收、发布记录与回滚。

用法：deploy/upgrade.sh [选项] [-- <透传给 install.sh 的参数>]

  --src=<dir>          常驻源码目录（默认：本脚本所在仓库）。第一次可以指向一个不存在的目录
  --remote=<name>      取码用的 remote 名，默认 origin
  --remote-url=<url>   源码仓库地址；给了它且 --src 目录不存在时自动 clone（免手工建目录）
  --ref=<git ref>      要发布的分支/tag/commit：自动 fetch + detach checkout + 绑定 TAG
  --no-fetch           配合 --ref 用：只用本地已有的对象，不访问远端
  --allow-dirty        允许带未提交的跟踪文件改动发布（TAG 会加 -dirty 后缀）
  --tag=<tag>          手工指定镜像 TAG（导出的 TAG 等效，均不加后缀；默认由 git 派生：<install.env 的 TAG>-<短 sha>）
  --follow-env-tag     不加 commit 后缀，直接用 install.env 里的 TAG（正式发布号用这个）
  --no-build           不构建，直接用仓库里已有的镜像（重发/回滚用；默认同时 PUSH=false）
  --rollback           回到发布记录里上一个好版本（隐含 --no-build）
  --status             只看当前集群跑的是什么、上一次发布记了什么，不改动任何东西
  --diff               转发 install.sh --diff（kubectl diff 预演，不写集群）
  --dry-run            转发 install.sh --dry-run（只渲染清单，不碰集群）
  --state-dir=<dir>    发布记录目录，默认 ~/.aegisops/deploy（或环境变量 AEGISOPS_DEPLOY_STATE）
  --env-file=<path>    install.sh 参数文件，默认 <state-dir>/install.env，回落到 ./install.env
  --verify-timeout=<s> 验收等待秒数，默认 300
  -y, --yes            无人值守
  -h, --help           看这份说明

典型用法（把参数文件从源码目录里挪出来，源码目录就能常驻复用）：
  mkdir -p ~/.aegisops/deploy && cp deploy/install.env.example ~/.aegisops/deploy/install.env
  deploy/upgrade.sh --dry-run                      # 先看渲染结果
  deploy/upgrade.sh --ref=DEV-v4.0.0               # 拉最新代码 → 构建 → 发布 → 验收 → 记账
  deploy/upgrade.sh                                # 就发当前工作树（不 fetch、不 checkout）
  deploy/upgrade.sh --status                       # 现在跑的是哪个 commit
  deploy/upgrade.sh --rollback                     # 出问题回到上一个好版本
EOF
}

parse_args() {
  local arg key
  while [[ $# -gt 0 ]]; do
    arg="$1"
    case "$arg" in
      -h|--help) usage; exit 0 ;;
      --src=*) SRC_DIR="${arg#*=}" ;;
      --src) [[ $# -ge 2 ]] || die "--src 缺少取值" "例：--src=/root/aegisops-src"; SRC_DIR="$2"; shift ;;
      --state-dir=*) STATE_DIR="${arg#*=}" ;;
      --env-file=*) ENV_FILE="${arg#*=}" ;;
      --env-file) [[ $# -ge 2 ]] || die "--env-file 缺少取值" "例：--env-file ~/.aegisops/deploy/install.env"; ENV_FILE="$2"; shift ;;
      --remote=*) GIT_REMOTE="${arg#*=}" ;;
      --remote-url=*) GIT_REMOTE_URL="${arg#*=}" ;;
      --ref=*) REF="${arg#*=}" ;;
      --ref) [[ $# -ge 2 ]] || die "--ref 缺少取值" "例：--ref=v4.0.0 或 --ref=DEV-v4.0.0"; REF="$2"; shift ;;
      --tag=*) TAG="${arg#*=}" ;;
      --no-fetch) NO_FETCH=1 ;;
      --allow-dirty) ALLOW_DIRTY=1 ;;
      --follow-env-tag) FOLLOW_ENV_TAG=1 ;;
      --no-build) NO_BUILD=1 ;;
      --rollback) ROLLBACK=1; NO_BUILD=1 ;;
      --status) DO_STATUS=1 ;;
      --diff) DO_DIFF=1 ;;
      --dry-run) DRY_RUN=1 ;;
      --verify-timeout=*) VERIFY_TIMEOUT="${arg#*=}" ;;
      -y|--yes) ASSUME_YES=1 ;;
      --) shift; EXTRA_ARGS=("$@"); break ;;
      *) die "无法识别的参数：${arg}" "看 deploy/upgrade.sh --help；要透传给 install.sh 的参数放在 -- 之后" ;;
    esac
    shift
  done
}

# --- install.env 读取（只取发布决策要用的键，语义与 install.sh 的 load_env_file 一致）---
resolve_key() {
  # 优先级与 install.sh 对齐：导出的环境变量 > install.env > 调用方给的默认值。
  local key="$1" fallback="${2:-}" val=""
  if key_from_env "$key"; then
    val="${!key:-}"
  else
    val="$(env_value "$key")"
  fi
  [[ -n "$val" ]] || val="$fallback"
  printf '%s' "$val"
}

env_value() {
  local key="$1" file="${ENV_FILE_PATH:-}" out=""
  if [[ -n "$file" && -f "$file" ]]; then
    out="$(awk -v k="$key" '
      /^[ \t]*#/ { next }
      $0 ~ "^([ \t]*" k "[ \t]*=)" { v = substr($0, index($0, "=") + 1); last = v }
      END { if (last != "") print last }' "$file" | tail -n 1)"
    # 去掉成对引号与首尾空白（DSN 之类常带单引号）。
    out="${out#"${out%%[![:space:]]*}"}"; out="${out%"${out##*[![:space:]]}"}"
    if [[ ${#out} -ge 2 ]]; then
      if [[ "${out:0:1}" == '"' && "${out: -1}" == '"' ]] ||
         [[ "${out:0:1}" == "'" && "${out: -1}" == "'" ]]; then
        out="${out:1:${#out}-2}"
      fi
    fi
  fi
  printf '%s' "$out"
}

pick_env_file() {
  ENV_FILE_PATH=""
  local candidate
  if [[ -n "$ENV_FILE" ]]; then
    [[ -f "$ENV_FILE" ]] || die "--env-file 指向的文件不存在：${ENV_FILE}" "确认路径；参数文件建议放 <state-dir>/install.env，与源码目录解耦"
    ENV_FILE_PATH="$ENV_FILE"; return 0
  fi
  for candidate in "${STATE_DIR}/install.env" "${PWD}/install.env" "${SCRIPT_DIR}/install.env"; do
    if [[ -f "$candidate" ]]; then ENV_FILE_PATH="$candidate"; return 0; fi
  done
  die "找不到参数文件 install.env" "cp deploy/install.env.example ${STATE_DIR}/install.env 后填值（放 state-dir 里，换源码目录就不用重抄参数）"
}

# --- 取码：常驻目录 + 可选 fetch/checkout -----------------------------------
sync_source() {
  if [[ -z "$SRC_DIR" ]]; then SRC_DIR="$REPO_ROOT"; fi
  [[ -d "${SRC_DIR}/.git" ]] || {
    if [[ -n "$GIT_REMOTE_URL" ]]; then
      info "git clone ${GIT_REMOTE_URL} → ${SRC_DIR}"
      mkdir -p "$(dirname "$SRC_DIR")"
      git clone "$GIT_REMOTE_URL" "$SRC_DIR" >/dev/null || die "git clone 失败" "确认地址与凭据可访问后重跑"
    else
      die "源码目录不是 git 仓库：${SRC_DIR}" "给 --remote-url=<仓库地址> 让它自动 clone，或用 --src 指到已有仓库"
    fi
  }

  if [[ $NO_BUILD -eq 1 && -z "$REF" ]]; then
    # 回滚/重发不需要新代码，工作树脏不脏与本次发布无关。
    detail "--no-build 且未指定 --ref：跳过取码检查"
  else
    if [[ -n "$REF" && $NO_FETCH -eq 0 ]]; then
      info "git fetch（remote=${GIT_REMOTE}）"
      git -C "$SRC_DIR" fetch --all --tags --prune >/dev/null 2>&1 ||
        warn "git fetch 没成功（离线或 remote 不可达）：改用本地已有对象"
    fi
    local dirty
    dirty="$(git -C "$SRC_DIR" status --porcelain --untracked-files=no)"
    if [[ -n "$dirty" && $ALLOW_DIRTY -eq 0 && $DRY_RUN -eq 0 && $DO_DIFF -eq 0 ]]; then
      printf '%s\n' "$dirty" >&2
      die "源码目录有未提交的跟踪文件改动，拒绝按它发布" "先提交或还原；确认要发这个脏工作树就加 --allow-dirty（TAG 会带 -dirty）"
    fi
    if [[ -n "$dirty" ]]; then
      warn "工作树有未提交改动：本次产物带 -dirty 后缀，别把它当正式版本分发"
    fi
    if [[ -n "$REF" ]]; then
      local resolved="$REF"
      git -C "$SRC_DIR" rev-parse --verify --quiet "${REF}^{commit}" >/dev/null ||
        resolved="refs/remotes/${GIT_REMOTE}/${REF}"
      if ! git -C "$SRC_DIR" rev-parse --verify --quiet "${resolved}^{commit}" >/dev/null; then
        git -C "$SRC_DIR" fetch --tags "$GIT_REMOTE" "$REF" >/dev/null 2>&1 || true
        resolved="FETCH_HEAD"
      fi
      git -C "$SRC_DIR" rev-parse --verify --quiet "${resolved}^{commit}" >/dev/null ||
        die "解析不出要发布的 ref：${REF}" "git -C ${SRC_DIR} fetch --all --tags 后重试，或 git -C ${SRC_DIR} log --oneline -5 看本地有什么"
      info "checkout ${REF}（detach）"
      git -C "$SRC_DIR" checkout --detach "$resolved" >/dev/null ||
        die "checkout ${REF} 失败" "确认工作树没有冲突；必要时 --src 换一个新目录"
    fi
  fi

  TARGET_SHA="$(git -C "$SRC_DIR" rev-parse --short HEAD 2>/dev/null || echo none)"
  TARGET_REF="$REF"
  [[ -n "$TARGET_REF" ]] || TARGET_REF="$(git -C "$SRC_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo HEAD)"
}

compute_tag() {
  local base="${TAG}"
  if [[ $ROLLBACK -eq 1 ]]; then return 0; fi           # 回滚用记录里的 TAG
  if [[ -n "$base" ]]; then TARGET_TAG="$base"; return 0; fi
  base="$(env_value TAG)"
  [[ -n "$base" ]] || base="dev"
  if [[ $FOLLOW_ENV_TAG -eq 1 ]]; then
    TARGET_TAG="$base"
    return 0
  fi
  # 不可变 TAG：同一个 TAG 永不指向两个不同的 commit，IMAGE_PULL_POLICY=IfNotPresent 才不会命中旧镜像，
  # 回滚也才真的能回到那个二进制。
  local suffix="$TARGET_SHA"
  [[ -n "$(git -C "$SRC_DIR" status --porcelain --untracked-files=no 2>/dev/null)" ]] && suffix="${suffix}-dirty"
  TARGET_TAG="${base}-${suffix}"
}

# --- 发布记录（shell 可 source 的 key=value，不依赖 jq）---------------------
state_load() {
  STATE_FILE="${STATE_DIR}/${NAMESPACE}.env"
  HISTORY_FILE="${STATE_DIR}/history-${NAMESPACE}.log"
  LAST_GOOD_TAG=""; LAST_GOOD_SHA=""; LAST_GOOD_REF=""; LAST_GOOD_TS=""; LAST_GOOD_IMAGE=""
  if [[ -f "$STATE_FILE" ]]; then
    # 只取已知键，避免被历史里遗留的奇怪行影响。
    local line key value
    while IFS= read -r line || [[ -n "$line" ]]; do
      [[ "$line" == \#* || -z "$line" ]] && continue
      [[ "$line" != *=* ]] && continue
      key="${line%%=*}"; value="${line#*=}"
      case "$key" in
        LAST_GOOD_TAG|LAST_GOOD_SHA|LAST_GOOD_REF|LAST_GOOD_TS|LAST_GOOD_IMAGE) printf -v "$key" '%s' "$value" ;;
      esac
    done <"$STATE_FILE"
  fi
}

state_record_ok() {
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  {
    printf '# AegisOps 发布记录（由 deploy/upgrade.sh 维护，手工改请谨慎）\n'
    printf 'LAST_GOOD_TS=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'LAST_GOOD_REF=%s\n' "$TARGET_REF"
    printf 'LAST_GOOD_SHA=%s\n' "$TARGET_SHA"
    printf 'LAST_GOOD_TAG=%s\n' "$TARGET_TAG"
    printf 'LAST_GOOD_IMAGE=%s\n' "$IMAGE_BASE"
  } >"$STATE_FILE"
  printf '%s\tok\t%s\t%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$TARGET_TAG" "$TARGET_SHA" "$TARGET_REF" "${UPGRADE_LOG:-}" >>"$HISTORY_FILE"
}

state_record_fail() {
  mkdir -p "$STATE_DIR" 2>/dev/null || true
  printf '%s\tfail\t%s\t%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${TARGET_TAG:-?}" "${TARGET_SHA:-?}" "${TARGET_REF:-?}" "${UPGRADE_LOG:-}" >>"$HISTORY_FILE"
}

# --- 验收：Ready 由 readinessProbe(/readyz) 把关，版本再由 exec version 核对 --
verify_release() {
  local deps=(aegisops-server) dep out got
  [[ "$FORM" == "separated" ]] && deps+=(aegisops-web)
  for dep in "${deps[@]}"; do
    info "验收：等待 ${dep} Ready（readinessProbe 打的是 /readyz）"
    "$KUBECTL_BIN" -n "$NAMESPACE" rollout status "deployment/${dep}" --timeout="${VERIFY_TIMEOUT}s" || {
      warn "${dep} 没能在 ${VERIFY_TIMEOUT}s 内 Ready：kubectl -n ${NAMESPACE} logs deploy/${dep} --tail=80 看原因"
      return 1
    }
  done
  out="$("$KUBECTL_BIN" -n "$NAMESPACE" exec deploy/aegisops-server -- /usr/local/bin/aopsctl version 2>/dev/null || true)"
  got="$(printf '%s' "$out" | sed -n 's/.*commit \([0-9a-f]\{4,\}\).*/\1/p')"
  DEPLOYED_COMMIT="$got"
  if [[ -z "$got" ]]; then
    warn "没能从 aopsctl version 里读到 commit（镜像可能是手工构建的）：只核对了 Ready 状态"
  elif [[ "$got" == "$TARGET_SHA" || "$TARGET_SHA" == "none" || "$got" == "$TARGET_SHA"* ]]; then
    ok "版本核对通过：${out}"
  else
    warn "集群里跑的是 commit ${got}，本次目标是 ${TARGET_SHA}：多半是同名 TAG 被复用或 IMAGE_PULL_POLICY=IfNotPresent 命中旧镜像"
  fi
  return 0
}

show_status() {
  local cur=""
  cur="$("$KUBECTL_BIN" -n "$NAMESPACE" exec deploy/aegisops-server -- /usr/local/bin/aopsctl version 2>/dev/null || echo '（读不到：后端没起来或不在本命名空间）')"
  _emit ""
  _emit "当前命名空间 ${NAMESPACE}"
  _emit "  集群里   ：${cur}"
  _emit "  镜像     ：$("$KUBECTL_BIN" -n "$NAMESPACE" get deploy aegisops-server -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo 未知)"
  if [[ "$FORM" == "separated" ]]; then
    _emit "  前端镜像 ：$("$KUBECTL_BIN" -n "$NAMESPACE" get deploy aegisops-web -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo 未知)"
  fi
  _emit "  发布记录 ：${STATE_FILE}"
  if [[ -n "${LAST_GOOD_TAG:-}" ]]; then
    _emit "  上次成功 ：${LAST_GOOD_TAG}  commit=${LAST_GOOD_SHA}  ref=${LAST_GOOD_REF}  ${LAST_GOOD_TS}"
  else
    _emit "  上次成功 ：无记录（--rollback 需要它，先用本脚本发一次才会记账）"
  fi
  if [[ -f "$HISTORY_FILE" ]]; then
    _emit "  历史（最近 5 条）："
    tail -n 5 "$HISTORY_FILE" | sed 's/^/    /'
  fi
  _emit ""
}

confirm() {
  local question="$1" answer=""
  [[ $ASSUME_YES -eq 1 ]] && return 0
  if [[ ! -t 0 ]]; then
    die "需要确认但不是交互终端：${question}" "确认后加 --yes 重跑"
  fi
  printf '%s [y/N] ' "$question"
  read -r answer
  [[ "$answer" == "y" || "$answer" == "Y" ]]
}

run_install() {
  local args=(--env-file "$ENV_FILE_PATH")
  [[ $ASSUME_YES -eq 1 ]] && args+=(--yes)
  [[ $DRY_RUN -eq 1 ]] && args+=(--dry-run)
  [[ $DO_DIFF -eq 1 ]] && args+=(--diff)
  if [[ $NO_BUILD -eq 1 ]]; then
    args+=(--no-build)
    # 回滚/重发的镜像本就在仓库里，再 push 一次只会因为本地没有该镜像而失败。
    key_from_env PUSH || args+=(--no-push)
  fi
  if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then args+=("${EXTRA_ARGS[@]}"); fi

  # TAG 走环境变量：install.sh 的优先级是 CLI > 环境变量 > env 文件，这样本脚本算出的
  # 不可变 TAG 能盖过 install.env 里写死的 TAG，而其它参数仍按用户的文件走。
  if [[ -n "$TARGET_TAG" ]]; then export TAG="$TARGET_TAG"; fi
  info "调用 install.sh（TAG=${TARGET_TAG:-<沿用 install.env>}，形态=${FORM}，命名空间=${NAMESPACE}）"
  # install.sh 把日志写在当前目录，切到 state/logs 下执行，日志与源码目录一起解耦。
  mkdir -p "$LOG_DIR"
  ( cd "$LOG_DIR" && "$INSTALL_SH" "${args[@]}" )
}

main() {
  parse_args "$@"
  command -v git >/dev/null 2>&1 || die "找不到 git" "本脚本靠 git 决定发布内容；只想换镜像请在源码目录直接跑 deploy/install.sh"
  [[ -f "$INSTALL_SH" ]] || die "缺少 ${INSTALL_SH}" "确认 deploy/ 目录完整"

  pick_env_file
  NAMESPACE="$(resolve_key NAMESPACE aegisops)"
  FORM="$(resolve_key FORM allinone)"
  IMAGE_BASE="$(resolve_key IMAGE)"
  WEB_IMAGE_BASE="$(resolve_key IMAGE_WEB)"
  STATE_FILE="${STATE_DIR}/${NAMESPACE}.env"
  HISTORY_FILE="${STATE_DIR}/history-${NAMESPACE}.log"
  LOG_DIR="${STATE_DIR}/logs"
  mkdir -p "$STATE_DIR" "$LOG_DIR" 2>/dev/null || true
  UPGRADE_LOG="${LOG_DIR}/upgrade-${NAMESPACE}-${RUN_TS}.log"
  if [[ "${AEGISOPS_INSTALL_NO_TEE:-0}" != "1" ]]; then
    exec > >(tee -a "$UPGRADE_LOG") 2>&1
  fi

  state_load
  if [[ $DO_STATUS -eq 1 ]]; then show_status; return 0; fi

  if [[ $ROLLBACK -eq 1 ]]; then
    [[ -n "${LAST_GOOD_TAG:-}" ]] || die "没有可回滚的发布记录：${STATE_FILE} 不存在或为空" "用 deploy/upgrade.sh 正常发一次，之后才有 --rollback 的落点"
    TARGET_TAG="$LAST_GOOD_TAG"; TARGET_SHA="${LAST_GOOD_SHA:-none}"; TARGET_REF="${LAST_GOOD_REF:-recorded}"
    _emit ""
    _emit "${C_YLW}回滚${C_OFF}  命名空间=${NAMESPACE}  目标 TAG=${TARGET_TAG}  commit=${TARGET_SHA}（记录于 ${LAST_GOOD_TS}）"
    confirm "确认把 ${NAMESPACE} 回滚到 ${TARGET_TAG}？" || { warn "已放弃回滚，集群未做任何改动"; return 0; }
    sync_source
    run_install
    if verify_release; then
      ok "回滚完成：现在跑的是 ${TARGET_TAG}"
    else
      state_record_fail
      die "回滚后验收没通过" "kubectl -n ${NAMESPACE} get pods；kubectl -n ${NAMESPACE} logs deploy/aegisops-server --tail=80"
    fi
    return 0
  fi

  sync_source
  compute_tag
  [[ -n "$IMAGE_BASE" ]] || die "参数文件里没有 IMAGE" "在 ${ENV_FILE_PATH} 里写 IMAGE=<仓库地址>/aegisops（必填）"

  _emit ""
  _emit "${C_BLU}aegisops 发布${C_OFF}  命名空间=${NAMESPACE}  形态=${FORM}"
  _emit "  源码目录  ：${SRC_DIR}（ref=${TARGET_REF} commit=${TARGET_SHA}）"
  _emit "  镜像      ：${IMAGE_BASE}:${TARGET_TAG}${WEB_IMAGE_BASE:+ / ${WEB_IMAGE_BASE}:${TARGET_TAG}}"
  _emit "  参数文件  ：${ENV_FILE_PATH}"
  _emit "  发布记录  ：${STATE_FILE}"
  if [[ -n "${LAST_GOOD_TAG:-}" ]]; then _emit "  上一个版本：${LAST_GOOD_TAG}（commit ${LAST_GOOD_SHA:-?}）"; fi
  if [[ $DRY_RUN -eq 1 || $DO_DIFF -eq 1 ]]; then
    info "预演模式：不写集群、不记账"
  elif ! confirm "确认发布？"; then
    warn "已放弃发布，集群未做任何改动"; return 0
  fi

  if ! run_install; then
    state_record_fail
    _emit ""
    if [[ -n "${LAST_GOOD_TAG:-}" ]]; then
      _emit "  回退上一条好版本：deploy/upgrade.sh --rollback --env-file ${ENV_FILE_PATH}"
    fi
    die "install.sh 未成功结束（详见上面的阶段报错与根因）" "按 install.sh 给的下一步处理；改完原地重跑本脚本即可"
  fi

  if [[ $DRY_RUN -eq 1 || $DO_DIFF -eq 1 ]]; then
    ok "预演完成，集群未改动（不写发布记录）"
    return 0
  fi

  if ! verify_release; then
    state_record_fail
    _emit ""
    if [[ -n "${LAST_GOOD_TAG:-}" ]]; then
      _emit "  回退上一条好版本：deploy/upgrade.sh --rollback --env-file ${ENV_FILE_PATH}"
    fi
    die "发布后验收没通过：集群仍处在不可用状态" "先 kubectl -n ${NAMESPACE} describe pod + logs 定位；修不好就按上面一条回退"
  fi
  state_record_ok
  _emit ""
  ok "发布完成：${IMAGE_BASE}:${TARGET_TAG}（commit ${TARGET_SHA}）"
  _emit "  已记账：${STATE_FILE}"
  _emit "  出问题回退：deploy/upgrade.sh --rollback"
}

main "$@"
