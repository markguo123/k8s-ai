#!/usr/bin/env bash
# =============================================================================
# k8s-ai 四期一键安装脚本（开发计划 P6.4）
#
# 一句话：把「构建镜像 → 前置检查 → 基础资源 → Secret → PVC → 迁移 Job →
# 工作负载 → 对外暴露 → 写权限 → 验收摘要」串成一条可重复执行的命令，
# 运维不需要手改任何 YAML。
#
# 口径唯一来源：docs/03-运维与部署/01-部署与安装.md §4.10。改行为先改文档。
#
# 红线（后续改动别破坏，tests/arch/install_script_test.go 有门禁）：
#   1. 脚本本体零 LLM、零外呼：没有模型时纯参数也必须装通（ADR-045）。
#   2. 仓库里的 YAML 永不被就地改写：替换只发生在 mktemp -d 的渲染副本里，
#      正式清单必须继续能独立 kubectl apply。
#   3. 只替换已知锚点（namespace / image / imagePullPolicy / storageClassName /
#      Ingress host 与 class / Service 类型与端口 / 拉取凭据 / blob env），
#      渲染产物断言零 REPLACE_ 残留、零示例域名残留。
#   4. 密钥复用优先：已存在的 Secret 一律不覆盖；轮换只走显式 --rotate。
#   5. DSN 与口令经 stdin 注入，不进 ps、不进 shell history。
#   6. 必填项缺失一律拒绝并说明缺哪个，绝不猜默认值。
#   7. 环境专有值（真实仓库地址、真实域名、真实存储类名）不进仓库。
# =============================================================================
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_TS="$(date +%Y%m%d-%H%M%S)"
LOG_FILE="${PWD}/install-${RUN_TS}.log"
RENDER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/k8s-ai-install.XXXXXX")"
KEEP_RENDER=0

# 进入脚本时环境里已有的变量名。必须在下面赋内置默认值之前抓快照：内置默认值只是普通
# 变量赋值，不导出，所以这张表就是「用户显式给的环境变量」。env 文件靠它判断该不该覆盖，
# 否则有非空默认值的键（NAMESPACE 等）永远无法被 env 文件设置。
ENV_VAR_NAMES=" $(compgen -e | tr '\n' ' ') "
key_from_env() { [[ "$ENV_VAR_NAMES" == *" $1 "* ]]; }

# CLI 显式给过的键。env 文件是在参数解析之后才加载的，靠这张表保证 CLI 永远压过文件。
CLI_SET_KEYS=" "
key_from_cli() { [[ "$CLI_SET_KEYS" == *" $1 "* ]]; }

# --- 全部可配置参数（内置默认值是四层优先级的最后一层）----------------------
# 必填（★）在下面 validate_config 里校验：IMAGE / TAG，条件必填见同函数。
# 一律用 ": "${KEY:=默认值}"" 形式：环境变量已经给了值就保留用户的值，否则才落默认值。
# 直接 KEY=默认 会无条件冲掉导出的环境变量，那样「环境变量压过默认」就是空话。
: "${IMAGE:=}"
: "${TAG:=}"
: "${IMAGE_WEB:=}"
: "${BUILD:=true}"
: "${PUSH:=true}"
: "${LOAD_TAR:=}"
: "${NAMESPACE:=k8s-ai}"
: "${FORM:=allinone}"
: "${STORE_DRIVER:=mysql}"
: "${MYSQL_DSN:=}"
: "${SQLITE_PATH:=/data/k8s-ai.db}"
: "${STORAGE_CLASS:=}"
: "${REPORT_STORAGE_SIZE:=5Gi}"
: "${PERSIST_REPORTS:=true}"
: "${EXPOSE:=nodeport}"
: "${NODE_PORT:=}"
: "${INGRESS_HOST:=}"
: "${INGRESS_CLASS:=}"
: "${INGRESS_CLASS_DEFAULT:=nginx}"
: "${ENABLE_WRITE:=false}"
: "${ADMIN_USERNAME:=}"
: "${ADMIN_PASSWORD:=}"
: "${LLM_API_KEY:=}"
: "${IMAGE_PULL_SECRET:=}"
: "${IMAGE_PULL_POLICY:=IfNotPresent}"
: "${BLOBS_BACKEND:=db}"
: "${BLOBS_ENDPOINT:=}"
: "${BLOBS_BUCKET:=}"
: "${BLOBS_ACCESS_KEY:=}"
: "${BLOBS_SECRET_KEY:=}"
: "${BLOBS_REGION:=us-east-1}"
: "${BLOBS_PATH_STYLE:=true}"
: "${BLOBS_USE_TLS:=false}"
: "${BLOBS_CREATE_BUCKET:=true}"
: "${BLOBS_PREFIX:=}"
: "${ENABLE_MINIO:=false}"
: "${MINIO_STORAGE_SIZE:=20Gi}"
: "${ENC_KEY:=}"
: "${ENC_KEY_BACKUP_DIR:=${HOME}/.k8s-ai/keys}"
: "${GOPROXY:=}"
: "${KUBECTL_VERSION:=v1.28.13}"
# 与仓库 Dockerfile 默认值一致：公共镜像加速地址；内网用 --KUBECTL_DOWNLOAD_BASE 覆盖。
: "${KUBECTL_DOWNLOAD_BASE:=https://files.m.daocloud.io/dl.k8s.io/release}"
: "${WAIT_TIMEOUT:=300}"

# 参数名清单：env 文件与通用 --KEY=VALUE 都按这张表识别，避免拼错键被静默忽略。
PARAM_KEYS="IMAGE TAG IMAGE_WEB BUILD PUSH LOAD_TAR NAMESPACE FORM STORE_DRIVER MYSQL_DSN
SQLITE_PATH STORAGE_CLASS REPORT_STORAGE_SIZE PERSIST_REPORTS EXPOSE NODE_PORT INGRESS_HOST
INGRESS_CLASS ENABLE_WRITE ADMIN_USERNAME ADMIN_PASSWORD LLM_API_KEY IMAGE_PULL_SECRET
IMAGE_PULL_POLICY BLOBS_BACKEND BLOBS_ENDPOINT BLOBS_BUCKET BLOBS_ACCESS_KEY BLOBS_SECRET_KEY
BLOBS_REGION BLOBS_PATH_STYLE ENABLE_MINIO MINIO_STORAGE_SIZE ENC_KEY ENC_KEY_BACKUP_DIR
BLOBS_USE_TLS BLOBS_CREATE_BUCKET BLOBS_PREFIX GOPROXY KUBECTL_VERSION KUBECTL_DOWNLOAD_BASE
WAIT_TIMEOUT"

# 换行展平后的白名单副本：case 匹配要求键两侧是空格，直接用多行原文会漏判行尾键。
PARAM_KEYS_FLAT=" ${PARAM_KEYS//$'\n'/ } "

# --- 运行期状态 --------------------------------------------------------------
DRY_RUN=0
DO_DIFF=0
ASSUME_YES=0
DO_ROTATE=0
VERBOSE=0
ENV_FILE=""
ENV_FILE_PATH=""
CURRENT_STAGE="前置准备"
STAGE_T0=0
ADMIN_PASSWORD_SHOWN=""
ENC_KEY_BACKUP_PATH=""
ENC_KEY_ORIGIN=""
SERVER_URL=""
WEB_URL=""
ACTUAL_NODE_PORT=""
PULLED_SECRET_NAME=""

SEC_STORE=k8s-ai-store
SEC_ENC=k8s-ai-enc
SEC_ADMIN=k8s-ai-admin
SEC_LLM=k8s-ai-llm
SEC_BLOBS=k8s-ai-blobs
PVC_NAME=k8s-ai-data

# --- 输出与日志 --------------------------------------------------------------
if [[ -t 1 ]]; then
  C_DIM=$'\033[2m'; C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'; C_BLU=$'\033[36m'; C_OFF=$'\033[0m'
else
  C_DIM=""; C_RED=""; C_GRN=""; C_YLW=""; C_BLU=""; C_OFF=""
fi

_emit() { printf '%s\n' "$*"; }
info() { _emit "${C_BLU}[信息]${C_OFF} $*"; }
ok() { _emit "${C_GRN}[ ok ]${C_OFF} $*"; }
warn() { _emit "${C_YLW}[警告]${C_OFF} $*" >&2; }
detail() { if [[ $VERBOSE -eq 1 ]]; then _emit "${C_DIM}       $*${C_OFF}"; fi; }

# 失败三段式：阶段名 / 根因摘要 / 下一条可复制命令。
die() {
  local cause="$1" next="${2:-}"
  _emit "" >&2
  _emit "${C_RED}安装失败${C_OFF}" >&2
  _emit "  阶段  ：${CURRENT_STAGE}" >&2
  _emit "  根因  ：${cause}" >&2
  if [[ -n "$next" ]]; then _emit "  下一步：${next}" >&2; fi
  _emit "  日志  ：${LOG_FILE}" >&2
  _emit "" >&2
  exit 1
}

stage() {
  CURRENT_STAGE="$1"
  STAGE_T0="$(date +%s)"
  _emit ""
  _emit "${C_BLU}=== ${1} ===${C_OFF}"
}

stage_done() {
  local cost=$(( $(date +%s) - STAGE_T0 ))
  _emit "${C_GRN}--- ${CURRENT_STAGE} 完成（${cost}s）---${C_OFF}"
}

confirm() {
  local question="$1" answer=""
  if [[ $ASSUME_YES -eq 1 ]]; then
    info "已用 --yes 无人值守，默认确认：${question}"
    return 0
  fi
  if [[ ! -t 0 ]]; then
    die "需要交互确认但当前不是交互终端：${question}" "在交互终端重跑，或加 --yes 明确无人值守"
  fi
  printf '%s [y/N] ' "$question"
  read -r answer
  [[ "$answer" == "y" || "$answer" == "Y" ]]
}

on_exit() {
  local rc=$?
  if [[ $KEEP_RENDER -eq 1 ]]; then
    _emit "${C_DIM}渲染产物保留在：${RENDER_DIR}${C_OFF}"
  else
    rm -rf "$RENDER_DIR" 2>/dev/null || true
  fi
  return $rc
}
trap on_exit EXIT

# --- 随机值生成（无 openssl 时退化到 urandom / $RANDOM）---------------------
gen_hex32() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32; return 0
  fi
  if command -v od >/dev/null 2>&1 && [[ -r /dev/urandom ]]; then
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; echo; return 0
  fi
  local out="" i
  for i in $(seq 1 32); do out+="$(printf '%02x' $((RANDOM % 256)))"; done
  printf '%s\n' "$out"
}

gen_password() {
  local raw=""
  if command -v openssl >/dev/null 2>&1; then
    raw="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | cut -c1-20)"
  fi
  while [[ ${#raw} -lt 20 ]]; do
    raw+="$(printf '%s%s' "${RANDOM}" "${RANDOM}" | cut -c1-8)"
  done
  printf '%s\n' "${raw:0:20}"
}

# 密钥落盘备份：600 权限，只在本机留一份。
backup_key() {
  local kind="$1" value="$2" dir file
  dir="$ENC_KEY_BACKUP_DIR"
  mkdir -p "$dir" 2>/dev/null || die "无法创建密钥备份目录 ${dir}" "用 ENC_KEY_BACKUP_DIR 指定一个可写目录后重跑"
  file="${dir}/${kind}.${NAMESPACE}.${RUN_TS}.key"
  ( umask 077; printf '%s\n' "$value" >"$file" )
  chmod 600 "$file" 2>/dev/null || true
  printf '%s\n' "$file"
}

# --- 使用帮助 ----------------------------------------------------------------
usage() {
  cat <<'EOF'
k8s-ai 四期一键安装脚本（P6.4）：构建镜像 + 发布服务，一条命令装完，不需要手改 YAML。

用法：
  deploy/install.sh [参数...] [模式开关]

模式开关：
  --env-file <path>   从 KEY=VALUE 文件读参数（等价 install.env）
  --dry-run           只渲染清单到临时目录 + 占位符残留断言，完全不碰集群
  --diff              渲染后走 kubectl diff 预演，不做任何写入
  -v, --verbose       展开各阶段原始 kubectl / docker 输出
  -y, --yes           无人值守：不提问（写权限等危险动作仍要求显式参数）
  --rotate            仅轮换 K8S_AI_ENC_KEY（非交互模式下直接拒绝）
  -h, --help          本帮助

参数写法（三种等价，优先级 CLI > 环境变量 > env 文件 > 内置默认）：
  --IMAGE=<addr>     命令行传参（注意必须带等号，--IMAGE <addr> 这种空格写法不支持）
  IMAGE=<addr>       导出环境变量
  写进 --env-file    KEY=VALUE 文件，每行一个；值可以带成对引号，脚本会去掉

参数（★ = 必填，缺失即拒绝，脚本不猜默认值）：
  IMAGE★                后端镜像地址，不含标签，例 registry.local/k8s-ai
  TAG★                  镜像标签，例 4.0.0
  IMAGE_WEB             前端镜像地址（FORM=separated 必填），不含标签
  BUILD                 是否由脚本构建镜像，默认 true（探测不到容器运行时自动降级为只发布）
  PUSH                  构建后是否推送，默认 true
  LOAD_TAR              离线导入：docker load -i <tar> 后按 IMAGE:TAG 发布，跳过构建
  NAMESPACE             命名空间，默认 k8s-ai
  FORM                  allinone（形态 A 一体化，默认）/ separated（形态 B 前后端分离）
  STORE_DRIVER          mysql（默认）/ sqlite（单机兜底，必须配 PVC）
  MYSQL_DSN★            STORE_DRIVER=mysql 时必填；只写进 Secret，不进 ConfigMap
  SQLITE_PATH           sqlite 库文件路径，默认 /data/k8s-ai.db（/data 必须落 PVC）
  STORAGE_CLASS         空 = 使用集群默认存储类（不是不建 PVC）；给了名字就用它
  REPORT_STORAGE_SIZE   数据/报告 PVC 容量，默认 5Gi
  PERSIST_REPORTS       默认 true；false 时 /reports 退化 emptyDir（重启即清）
  EXPOSE                nodeport（默认）/ ingress；给了 INGRESS_HOST 则两者并存
  NODE_PORT             空 = 集群在 NodePort 区间随机分配，末行回显实际端口与节点 URL
  INGRESS_HOST          给了才建 Ingress（自定义域名需要客户端能解析）
  INGRESS_CLASS         IngressClass 名，留空用 nginx；先 kubectl get ingressclass 确认现场类名
  ENABLE_WRITE          true 时显式 apply 写权限 RBAC，并二次确认；默认 false 只读
  ADMIN_USERNAME        首启管理员用户名，留空用配置中心 platform.auth.admin_username
  ADMIN_PASSWORD        缺省随机生成，只打印一次，不落文件；重跑不覆盖已存在口令
  LLM_API_KEY           可选，首启播种模型密钥（密钥态：env 恒优先于页面值）
  IMAGE_PULL_SECRET     私有仓库拉取凭据：已有 Secret 名，或 dockerconfigjson 文件路径
  IMAGE_PULL_POLICY     IfNotPresent（默认）/ Always（用 :latest 这类可变标签时建议）
  BLOBS_BACKEND         off / db（默认）/ dir / s3（P8 对象存储边界）
  BLOBS_ENDPOINT        s3 必填，host:port 形式
  BLOBS_BUCKET          s3 必填，桶名
  BLOBS_ACCESS_KEY      s3 必填，只落 Secret/k8s-ai-blobs，不进清单明文
  BLOBS_SECRET_KEY      s3 必填，同上；留空会被 validate 拒绝
  BLOBS_USE_TLS         默认 false（集群内自建 MinIO 常见 http）
  BLOBS_CREATE_BUCKET   默认 true；无建桶权限的现场置 false 并预建桶
  BLOBS_PREFIX          对象键首段，留空用配置默认 k8sai
  BLOBS_REGION          默认 us-east-1；MinIO 等自建填 us-east-1 即可
  BLOBS_PATH_STYLE      默认 true（MinIO/自建需要路径式寻址）
  ENABLE_MINIO          默认 false；true 需要 P8.8 的自带 MinIO 清单，当前版本明确拒绝
  MINIO_STORAGE_SIZE    自带 MinIO 容量，默认 20Gi
  ENC_KEY               显式指定 K8S_AI_ENC_KEY（64 位 hex）；缺省复用集群已有值或新生成
  ENC_KEY_BACKUP_DIR    新生成密钥的本地备份目录，默认 ~/.k8s-ai/keys（权限 600）
  GOPROXY               构建用 Go 代理；留空则读 ~/.k8s-ai/goproxy.txt 或用内置默认
  KUBECTL_VERSION       内置进镜像的 kubectl 版本，空 = 不内置（保留 shell 工具层）
  KUBECTL_DOWNLOAD_BASE kubectl 下载地址前缀，内网 mirror 用（目录结构同 dl.k8s.io/release）
  WAIT_TIMEOUT          rollout / wait 超时秒数，默认 300

示例：
  # 最小可用：一条命令装完（NodePort 暴露、PVC 走集群默认存储类、只读 RBAC）
  IMAGE=registry.local/k8s-ai TAG=4.0.0 MYSQL_DSN='user:pass@tcp(db:3306)/k8s_ai' deploy/install.sh

  # 反复重跑：参数写进 install.env
  deploy/install.sh --env-file install.env

  # 预演 / 排障
  deploy/install.sh --env-file install.env --dry-run
  deploy/install.sh --env-file install.env --diff
  deploy/install.sh --env-file install.env --rotate
EOF
}

# --- 参数装配：内置默认 < env 文件 < 环境变量 < CLI -------------------------
# 实现顺序：环境里已有的值不被覆盖；env 文件只填环境里没给的；CLI 最后硬赋值。
set_param() {
  local key="$1" value="$2" src="$3"
  case "$PARAM_KEYS_FLAT" in
    *" ${key} "*) ;;
    *) die "未知参数 --${key}" "对照 deploy/install.sh --help 里的参数名；拼错的键会被静默忽略，所以这里直接拒绝" ;;
  esac
  if [[ "$src" == "CLI" ]]; then CLI_SET_KEYS+="${key} "; fi
  printf -v "$key" '%s' "$value"
}

load_env_file() {
  local file="$1" line key value lineno=0
  [[ -f "$file" ]] || die "--env-file 指向的文件不存在：${file}" "确认路径，或删掉该参数改用环境变量"
  ENV_FILE_PATH="$file"
  while IFS= read -r line || [[ -n "$line" ]]; do
    lineno=$((lineno + 1))
    line="${line%$'\r'}"
    [[ -z "${line//[[:space:]]/}" ]] && continue
    [[ "${line#"${line%%[![:space:]]*}"}" == "#"* ]] && continue
    [[ "$line" != *"="* ]] && die "${file}:${lineno} 不是 KEY=VALUE 形式" "删掉这一行，或改成 KEY=VALUE"
    key="${line%%=*}"
    value="${line#*=}"
    key="${key//[[:space:]]/}"
    # 值支持成对引号：DSN 之类含特殊字符的值按 shell 直觉会被引起来，这里去掉引号。
    if [[ ${#value} -ge 2 ]]; then
      if [[ "${value:0:1}" == '"' && "${value: -1}" == '"' ]] ||
         [[ "${value:0:1}" == "'" && "${value: -1}" == "'" ]]; then
        value="${value:1:${#value}-2}"
      fi
    fi
    case "$PARAM_KEYS_FLAT" in
      *" ${key} "*) ;;
      *) die "${file}:${lineno} 出现未知参数 ${key}" "对照 deploy/install.sh --help；拼错的键会被静默忽略，所以这里直接拒绝" ;;
    esac
    # 优先级 CLI > 环境变量 > 文件 > 内置默认：只有后两者才允许被文件写。
    if ! key_from_env "$key" && ! key_from_cli "$key"; then
      printf -v "$key" '%s' "$value"
    fi
  done <"$file"
}

parse_args() {
  local arg key candidate
  while [[ $# -gt 0 ]]; do
    arg="$1"
    case "$arg" in
      -h|--help) usage; exit 0 ;;
      --dry-run) DRY_RUN=1 ;;
      --diff) DO_DIFF=1 ;;
      -v|--verbose) VERBOSE=1 ;;
      -y|--yes) ASSUME_YES=1 ;;
      --no-build) BUILD=false ;;
      --no-push) PUSH=false ;;
      --rotate) DO_ROTATE=1 ;;
      --env-file)
        [[ $# -ge 2 ]] || die "--env-file 缺少取值" "例：--env-file install.env"
        ENV_FILE="$2"; shift ;;
      --env-file=*) ENV_FILE="${arg#*=}" ;;
      --load-tar)
        [[ $# -ge 2 ]] || die "--load-tar 缺少取值" "例：--load-tar ./k8s-ai-4.0.0.tar"
        LOAD_TAR="$2"; shift ;;
      --load-tar=*) LOAD_TAR="${arg#*=}" ;;
      --*=*)
        key="${arg%%=*}"; key="${key#--}"; key="${key//-/_}"; key="${key^^}"
        set_param "$key" "${arg#*=}" "CLI" ;;
      --*) die "无法识别的参数：${arg}" "参数写法是 --KEY=VALUE，见 deploy/install.sh --help" ;;
      *) die "多余的裸参数：${arg}" "本脚本只接受 --KEY=VALUE 与模式开关，见 --help" ;;
    esac
    shift
  done

  # env 文件：--env-file 优先，其次当前目录、脚本目录下的 install.env。
  if [[ -n "$ENV_FILE" ]]; then
    load_env_file "$ENV_FILE"
  else
    for candidate in "${PWD}/install.env" "${SCRIPT_DIR}/install.env"; do
      if [[ -f "$candidate" ]]; then load_env_file "$candidate"; break; fi
    done
  fi
}

require_param() {
  local key="$1" why="$2"
  if [[ -z "${!key:-}" ]]; then
    die "必填参数 ${key} 缺失。${why}" "例：--${key}=<值>，或写进 --env-file install.env"
  fi
}

require_bool() {
  local key="$1"
  case "${!key}" in
    true|false|TRUE|FALSE|1|0|yes|no) ;;
    *) die "${key} 只能是 true/false，当前值：${!key}" "例 --${key}=true" ;;
  esac
}

is_true() {
  case "${1:-}" in true|TRUE|1|yes|YES) return 0 ;; *) return 1 ;; esac
}

validate_config() {
  require_bool BUILD; require_bool PUSH; require_bool PERSIST_REPORTS
  require_bool ENABLE_WRITE; require_bool BLOBS_PATH_STYLE; require_bool ENABLE_MINIO
  require_bool BLOBS_USE_TLS; require_bool BLOBS_CREATE_BUCKET

  require_param IMAGE "镜像地址由现场仓库决定，脚本刻意不内置默认仓库。"
  require_param TAG "镜像标签缺失会导致拉到意外版本。"

  case "$FORM" in
    allinone|separated) ;;
    *) die "FORM 只能是 allinone 或 separated，当前值：${FORM}" "形态 A 用 --FORM=allinone，形态 B 用 --FORM=separated" ;;
  esac
  if [[ "$FORM" == "separated" ]]; then
    require_param IMAGE_WEB "形态 B 的前端是独立镜像，必须给地址。"
  fi

  case "$STORE_DRIVER" in
    mysql)
      require_param MYSQL_DSN "存储 DSN 含数据库口令，只写进 Secret；库与账号需 DBA 预建（建议 utf8mb4）。" ;;
    sqlite)
      if ! is_true "$PERSIST_REPORTS"; then
        die "STORE_DRIVER=sqlite 必须持久化：库文件在 /data，不挂 PVC 时 Pod 重启即丢库" \
          "去掉 --PERSIST_REPORTS=false，或改用 --STORE_DRIVER=mysql"
      fi ;;
    *) die "STORE_DRIVER 只能是 mysql 或 sqlite，当前值：${STORE_DRIVER}" "生产口径用 mysql" ;;
  esac

  case "$EXPOSE" in
    nodeport|ingress) ;;
    *) die "EXPOSE 只能是 nodeport 或 ingress，当前值：${EXPOSE}" "没有真实域名就用默认 nodeport" ;;
  esac
  if [[ -n "$NODE_PORT" ]] && [[ ! "$NODE_PORT" =~ ^[0-9]+$ ]]; then
    die "NODE_PORT 必须是数字，当前值：${NODE_PORT}" "留空表示让集群随机分配"
  fi
  if [[ -n "$NODE_PORT" ]] && { [[ "$NODE_PORT" -lt 30000 ]] || [[ "$NODE_PORT" -gt 32767 ]]; }; then
    die "NODE_PORT=${NODE_PORT} 不在 NodePort 默认区间 30000-32767" "换个端口，或留空由集群分配"
  fi
  if [[ "$EXPOSE" == "ingress" || -n "$INGRESS_HOST" ]]; then
    if [[ -z "$INGRESS_HOST" ]]; then
      die "EXPOSE=ingress 但 INGRESS_HOST 为空：没有 host 的 Ingress 毫无意义" \
        "给 --INGRESS_HOST=<域名> --INGRESS_CLASS=<class>，或改回 --EXPOSE=nodeport"
    fi
    [[ -n "$INGRESS_CLASS" ]] || INGRESS_CLASS="$INGRESS_CLASS_DEFAULT"
  fi

  case "$BLOBS_BACKEND" in
    off|db|dir|s3) ;;
    *) die "BLOBS_BACKEND 只能是 off/db/dir/s3，当前值：${BLOBS_BACKEND}" "默认 db 与旧版行为完全一致，不确定就别传" ;;
  esac
  if [[ "$BLOBS_BACKEND" == "s3" ]]; then
    require_param BLOBS_ENDPOINT "driver=s3 必须有 endpoint，格式 host:port 不带 scheme，例 minio.storage:9000"
    require_param BLOBS_BUCKET "driver=s3 必须有 bucket 名"
    require_param BLOBS_ACCESS_KEY "driver=s3 必须有 access key（只进 Secret）"
    require_param BLOBS_SECRET_KEY "driver=s3 必须有 secret key（只进 Secret）"
    [[ "$BLOBS_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] ||
      die "BLOBS_BUCKET 不是合法桶名：${BLOBS_BUCKET}" "桶名只允许小写字母、数字、点、连字符，长度 3-63"
    case "$BLOBS_ENDPOINT" in
      *'"'*|*'\'*|*'$'*|*'`'*) die "BLOBS_ENDPOINT 含非法字符（引号/反斜杠/美元符/反引号）" "检查是否误粘了 shell 引号" ;;
    esac
  fi
  if is_true "$ENABLE_MINIO"; then
    # 手册 §4.10 的两道互斥校验：自带 MinIO 隐含 s3；清单本身随 P8.8 交付。
    [[ "$BLOBS_BACKEND" == "s3" ]] ||
      die "ENABLE_MINIO=true 隐含要求 BLOBS_BACKEND=s3（自带 MinIO 就是给对象存储用的）" \
        "加 --BLOBS_BACKEND=s3 并补齐 BLOBS_* 四项，或保持 --ENABLE_MINIO=false"
    die "ENABLE_MINIO=true 需要的自带 MinIO 清单（deploy/base/minio.yaml）随开发计划 P8.8 交付，当前版本尚未落地" \
      "先接现场已有的 S3 兼容桶（--BLOBS_BACKEND=s3 指向它），或 --ENABLE_MINIO=false 用 db/dir 后端"
  fi

  if [[ -n "$ENC_KEY" ]] && [[ ! "$ENC_KEY" =~ ^[0-9a-fA-F]{64}$ ]]; then
    die "ENC_KEY 必须是 64 位 hex（32 字节）" "生成：openssl rand -hex 32；或删掉该参数让脚本复用/新生成"
  fi
  case "$IMAGE_PULL_POLICY" in
    IfNotPresent|Always|Never) ;;
    *) die "IMAGE_PULL_POLICY 只能是 IfNotPresent/Always/Never，当前值：${IMAGE_PULL_POLICY}" "内网仓库用 :latest 时建议 Always" ;;
  esac
}

# --- kubectl / 容器运行时封装 ------------------------------------------------
KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
CONTAINER_TOOL=""

pick_container_tool() {
  local cand
  for cand in docker podman; do
    if command -v "$cand" >/dev/null 2>&1; then CONTAINER_TOOL="$cand"; return 0; fi
  done
  return 1
}

# 只读探测：失败返回非 0，由调用方决定是警告还是终止（集群可能只是慢）。
try_kctl() { "$KUBECTL_BIN" "$@" >/dev/null 2>&1; }

# 会改变集群状态的调用：先把命令摊在日志里（密钥不走命令行，这里没有敏感值）。
kctl() {
  detail "\$ ${KUBECTL_BIN} $*"
  "$KUBECTL_BIN" "$@"
}

kctl_or_die() {
  local next="${2:-按上面 kubectl 报错处理后原地重跑同一命令}"
  detail "\$ ${KUBECTL_BIN} $1"
  "$KUBECTL_BIN" $1 || die "命令执行失败：${KUBECTL_BIN} $1" "$next"
}

# Secret 已存在与否（幂等基石：存在就绝不覆盖）。
secret_exists() { try_kctl -n "$NAMESPACE" get secret "$1"; }

secret_get_value() {
  local raw
  raw="$("$KUBECTL_BIN" -n "$NAMESPACE" get secret "$1" -o jsonpath="{.data.$2}" 2>/dev/null || true)"
  [[ -n "$raw" ]] || return 1
  printf '%s' "$raw" | base64 -d 2>/dev/null
}

# 从 stdin 建 Secret：值不进 ps、不进 shell history，再经 apply 幂等落库。
create_secret_from_stdin() {
  local name="$1" key="$2" value="$3"
  printf '%s' "$value" | "$KUBECTL_BIN" -n "$NAMESPACE" create secret generic "$name" \
    "--from-file=${key}=/dev/stdin" \
    --annotation k8s-ai/install-managed=true \
    --dry-run=client -o yaml | "$KUBECTL_BIN" apply -f - >/dev/null
}

# --- 清单渲染引擎 ------------------------------------------------------------
# 只认这些锚点，其余内容原样抄写；仓库清单永远不被改写。
read -r -d '' RENDER_AWK <<'AWK_EOF' || true
{ lines[++n] = $0 }
END {
  base = FILENAME; sub(/^.*\//, "", base)
  pulled = 0; blobs = 0; npdone = 0
  for (i = 1; i <= n; i++) {
    l = lines[i]; skip = 0

    # 锚点 1：命名空间（含 ClusterRoleBinding 的 subject namespace）
    if (l ~ /^[ \t]*namespace:[ \t]*k8s-ai[ \t]*$/) {
      p = l; sub(/namespace:.*$/, "", p); l = p "namespace: " NS
    }
    # 锚点 2：namespace.yaml 里的资源名（只在这个文件里改，Ingress 同名资源不受影响）
    if (base == "namespace.yaml" && l ~ /^  name:[ \t]*k8s-ai[ \t]*$/) l = "  name: " NS

    # 锚点 3：镜像地址与拉取策略
    if (l ~ /image:[ \t]*k8s-ai-web:latest/) {
      p = l; sub(/image:[ \t]*k8s-ai-web:latest.*$/, "", p); l = p "image: " WEBREF
    } else if (l ~ /image:[ \t]*k8s-ai:latest/) {
      p = l; sub(/image:[ \t]*k8s-ai:latest.*$/, "", p); l = p "image: " IMGREF
    }
    if (l ~ /^[ \t]*imagePullPolicy:/) {
      p = l; sub(/imagePullPolicy:.*$/, "", p); l = p "imagePullPolicy: " POLICY
    }

    # 锚点 4：Ingress host 与 class（host 注释里的示例域名一并替换，保证零残留）
    if (HOST != "") gsub(/k8s-ai\.example\.com/, HOST, l)
    if (CLASS != "" && l ~ /^[ \t]*ingressClassName:/) {
      p = l; sub(/ingressClassName:.*$/, "", p); l = p "ingressClassName: " CLASS
    }

    # 锚点 5：PVC 容量与存储类（SC 为空 = 删掉占位注释，走集群默认存储类）
    if (base == "pvc.yaml") {
      if (l ~ /^[ \t]*storage:[ \t]*[0-9]+Mi[ \t]*$/ || l ~ /^[ \t]*storage:[ \t]*[0-9]+Gi[ \t]*$/) {
        p = l; sub(/storage:.*$/, "", p); l = p "storage: " SIZE
      }
      if (l ~ /REPLACE_WITH_STORAGE_CLASS/) {
        if (SC != "") l = "  storageClassName: " SC; else skip = 1
      }
    }

    # 锚点 11：ConfigMap 里的存储驱动与 sqlite 路径（只在需要时改，mysql 口径原样不动）
    if (base == "configmap.yaml") {
      # 行尾还留着注释，所以只替换值本身，不动注释。
      if (DRIVER != "mysql" && l ~ /^[ \t]*driver:[ \t]*mysql([^A-Za-z0-9]|$)/) {
        p = l; sub(/mysql/, DRIVER, p); l = p
      }
      if (SQLITE != "" && l ~ /^[ \t]*sqlite_path:[ \t]*"/) {
        p = l; sub(/"[^"]*"/, "\"" SQLITE "\"", p); l = p
      }
    }

    # 锚点 6：私有仓库拉取凭据（清单默认不带这段，给了名字才插入）
    if (PULLSEC != "" && base == "deployment.yaml" && pulled == 0 && l ~ /^[ \t]*serviceAccountName:/) {
      pulled = 1
      p = l; sub(/serviceAccountName:.*$/, "", p)
      print p "imagePullSecrets:"
      print p "  - name: " PULLSEC
    }

    # 锚点 7：额外 env（BLOBS_* → K8S_AI_PLATFORM_BLOBS_* 等，密钥项只走 Secret 引用）
    if (EXTRAENV != "" && base == "deployment.yaml" && blobs == 0 && l ~ /^[ \t]*env:[ \t]*$/) {
      blobs = 1
      print l
      m = split(EXTRAENV, barr, "@@")
      for (j = 1; j <= m; j++) print barr[j]
      skip = 1
    }

    # 锚点 8：入口 Service 改 NodePort（只改真正对外那一个）
    if (base == EXPOSED && l ~ /^[ \t]*type:[ \t]*ClusterIP[ \t]*$/) {
      p = l; sub(/type:.*$/, "", p); l = p "type: NodePort"
    }

    # 锚点 9：reports / data 卷由 emptyDir 换成 PVC 声明
    if (i > 1 && l ~ /^[ \t]+emptyDir:[ \t]*\{\}[ \t]*$/) {
      prev = lines[i - 1]; want = ""
      if (prev ~ /-[ \t]+name:[ \t]*reports[ \t]*$/ && REPORTS_PVC == "true") want = "reports"
      if (prev ~ /-[ \t]+name:[ \t]*data[ \t]*$/ && DATA_PVC == "true") want = "data"
      if (want != "") {
        p = l; sub(/emptyDir:.*$/, "", p)
        print p "persistentVolumeClaim:"
        print p "  claimName: " PVC
        skip = 1
      }
    }

    if (!skip) {
      print l
      # 锚点 10：钉死 NodePort 端口号（NODE_PORT 为空则交给集群随机分配）
      if (base == EXPOSED && NODEPORT != "" && npdone == 0 && l ~ /^[ \t]+targetPort:/) {
        npdone = 1
        p = l; sub(/targetPort:.*$/, "", p)
        print p "nodePort: " NODEPORT
      }
    }
  }
}
AWK_EOF

# 需要额外注入的容器 env 行（用 @@ 连接后传给 awk，避免 -v 的转义歧义）。
# 只注入「启动级形态参数」：对象存储后端、首启管理员用户名；业务参数留在配置中心。
extra_env_block() {
  local indent="            " out=""
  case "$BLOBS_BACKEND" in
    db) return 0 ;;
    off)
      out+="${indent}- name: K8S_AI_PLATFORM_BLOBS_DRIVER${NL}  value: \"off\"" ;;
    dir)
      out+="${indent}- name: K8S_AI_PLATFORM_BLOBS_DRIVER${NL}  value: \"dir\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_DIR${NL}  value: \"/data/blobs\"" ;;
    s3)
      out+="${indent}- name: K8S_AI_PLATFORM_BLOBS_DRIVER${NL}  value: \"s3\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_ENDPOINT${NL}  value: \"${BLOBS_ENDPOINT}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_BUCKET${NL}  value: \"${BLOBS_BUCKET}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_REGION${NL}  value: \"${BLOBS_REGION}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_USE_TLS${NL}  value: \"${BLOBS_USE_TLS}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_PATH_STYLE${NL}  value: \"${BLOBS_PATH_STYLE}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_CREATE_BUCKET${NL}  value: \"${BLOBS_CREATE_BUCKET}\""
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_ACCESS_KEY${NL}  valueFrom:${NL}    secretKeyRef: { name: ${SEC_BLOBS}, key: access_key }"
      out+="@@${indent}- name: K8S_AI_PLATFORM_BLOBS_SECRET_KEY${NL}  valueFrom:${NL}    secretKeyRef: { name: ${SEC_BLOBS}, key: secret_key }" ;;
  esac
  if [[ -n "$BLOBS_PREFIX" ]]; then
    if [[ -n "$out" ]]; then out+="@@"; fi
    out+="${indent}- name: K8S_AI_PLATFORM_BLOBS_PREFIX${NL}  value: \"${BLOBS_PREFIX}\""
  fi
  if [[ -n "$ADMIN_USERNAME" ]]; then
    if [[ -n "$out" ]]; then out+="@@"; fi
    out+="${indent}- name: K8S_AI_PLATFORM_AUTH_ADMIN_USERNAME${NL}  value: \"${ADMIN_USERNAME}\""
  fi
  printf '%s' "$out"
}

NL=$'\n'

render_one() {
  local rel="$1"
  local src="${SCRIPT_DIR}/${rel}" dst="${RENDER_DIR}/${rel}"
  [[ -f "$src" ]] || die "缺少清单文件：deploy/${rel}" "确认 deploy/ 目录完整（git status 看是否漏文件）"
  mkdir -p "$(dirname "$dst")"
  awk -v NS="$NAMESPACE" -v IMGREF="$IMAGE_REF" -v WEBREF="$WEB_IMAGE_REF" -v POLICY="$IMAGE_PULL_POLICY" \
      -v HOST="$INGRESS_HOST" -v CLASS="$INGRESS_CLASS" -v SC="$STORAGE_CLASS" -v SIZE="$REPORT_STORAGE_SIZE" \
      -v PULLSEC="$PULLED_SECRET_NAME" -v EXPOSED="$EXPOSED_SVC" -v NODEPORT="$NODE_PORT" \
      -v REPORTS_PVC="$PERSIST_REPORTS" -v DATA_PVC="$DATA_ON_PVC" -v PVC="$PVC_NAME" \
      -v DRIVER="$STORE_DRIVER" -v SQLITE="$SQLITE_PATH" \
      -v EXTRAENV="$(extra_env_block)" \
      "$RENDER_AWK" "$src" >"$dst"
  detail "渲染 deploy/${rel} -> ${dst}"
}

# 产物红线：零 REPLACE_ 残留、零示例域名残留。
assert_rendered_clean() {
  local f hits bad=0
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    if hits="$(grep -n -E 'REPLACE_|k8s-ai\.example\.com' "$f" 2>/dev/null)"; then
      bad=1
      warn "$(basename "$f") 仍有占位符残留："
      printf '%s\n' "$hits" >&2
    fi
  done < <(find "$RENDER_DIR" -type f -name '*.yaml' | sort)
  if [[ $bad -eq 1 ]]; then
    die "渲染产物存在占位符残留（REPLACE_ 或示例域名），清单不可用" \
      "按上面行号补对应参数；确认是清单新增锚点的话，同步补进 render_one 与 tests/arch 门禁"
  fi
}

# 渲染本次要用的全部清单（--dry-run 到这一步就停）。
render_all() {
  local rel
  RENDER_LIST=()
  for rel in base/namespace.yaml base/serviceaccount.yaml base/clusterrole.yaml \
             base/clusterrolebinding.yaml base/configmap.yaml; do
    RENDER_LIST+=("$rel")
  done
  if is_true "$PERSIST_REPORTS"; then RENDER_LIST+=("base/pvc.yaml"); fi
  if [[ "$STORE_DRIVER" == "mysql" ]]; then RENDER_LIST+=("base/db-migrate-job.yaml"); fi
  if [[ "$FORM" == "allinone" ]]; then
    RENDER_LIST+=("allinone/deployment.yaml" "base/service.yaml")
    [[ -n "$INGRESS_HOST" ]] && RENDER_LIST+=("allinone/ingress.yaml")
  else
    RENDER_LIST+=("separated/deployment.yaml" "base/service.yaml" \
                  "separated/web-deployment.yaml" "separated/web-service.yaml")
    [[ -n "$INGRESS_HOST" ]] && RENDER_LIST+=("separated/ingress.yaml")
  fi
  is_true "$ENABLE_WRITE" && RENDER_LIST+=("rbac-write.example.yaml")

  for rel in "${RENDER_LIST[@]}"; do render_one "$rel"; done
  assert_rendered_clean
  ok "已渲染 ${#RENDER_LIST[@]} 份清单到 ${RENDER_DIR}（仓库清单未被改写）"
}

# 写入集群。--diff 走独立路径（run_diff），这里只负责真 apply。
apply_rendered() {
  local rel="$1"
  kctl apply -f "${RENDER_DIR}/${rel}" >/dev/null || die "apply 失败：deploy/${rel}" "kubectl describe 看事件；修完原地重跑"
}

# --diff：渲染后整批走 kubectl diff。返回码 1 只代表「有差异」，不是失败。
run_diff() {
  stage "预演差异（kubectl diff，不写入任何资源）"
  render_all
  local rel rc
  for rel in "${RENDER_LIST[@]}"; do
    rc=0
    info "diff deploy/${rel}"
    "$KUBECTL_BIN" diff -f "${RENDER_DIR}/${rel}" || rc=$?
    if [[ $rc -gt 1 ]]; then
      die "kubectl diff 失败：deploy/${rel}" "按上面的报错处理权限或清单问题；只想看渲染结果就用 --dry-run"
    fi
  done
  KEEP_RENDER=1
  ok "差异预演完成，未改动集群。渲染目录：${RENDER_DIR}"
  stage_done
}

# 两个键的 Secret（AK/SK 必须一次建好）：值先落到渲染目录里的 600 文件，用完即删。
create_secret_two_keys() {
  local name="$1" k1="$2" v1="$3" k2="$4" v2="$5" f1="${RENDER_DIR}/.k1" f2="${RENDER_DIR}/.k2"
  ( umask 077; printf '%s' "$v1" >"$f1"; printf '%s' "$v2" >"$f2" )
  "$KUBECTL_BIN" -n "$NAMESPACE" create secret generic "$name" \
    "--from-file=${k1}=${f1}" "--from-file=${k2}=${f2}" \
    --annotation k8s-ai/install-managed=true \
    --dry-run=client -o yaml | "$KUBECTL_BIN" apply -f - >/dev/null
  local rc=$?
  rm -f "$f1" "$f2"
  return $rc
}

node_addresses() {
  "$KUBECTL_BIN" get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' 2>/dev/null | sed '/^$/d'
}

# --- 阶段 2：命名空间 + RBAC + ConfigMap -------------------------------------
stage_base() {
  stage "阶段 2/9 命名空间 + RBAC + ConfigMap"
  info "清单渲染进临时目录 ${RENDER_DIR}（仓库 YAML 不被改写）"
  render_all
  local rel
  for rel in base/namespace.yaml base/serviceaccount.yaml base/clusterrole.yaml \
             base/clusterrolebinding.yaml base/configmap.yaml; do
    apply_rendered "$rel"
  done
  ok "命名空间 ${NAMESPACE} 与只读 RBAC、ConfigMap 就绪"
  stage_done
}

# --- 阶段 3：Secret（幂等）--------------------------------------------------
stage_secrets() {
  stage "阶段 3/9 Secret（已存在的一律不覆盖）"

  if secret_exists "$SEC_STORE"; then
    ok "Secret/${SEC_STORE} 已存在，复用既有 DSN"
  elif [[ "$STORE_DRIVER" == "mysql" ]]; then
    create_secret_from_stdin "$SEC_STORE" dsn "$MYSQL_DSN" || die "写入 Secret/${SEC_STORE} 失败" "确认当前身份有 secrets 写权限"
    ok "Secret/${SEC_STORE} 已创建（DSN 经 stdin 注入，未出现在命令行）"
  else
    create_secret_from_stdin "$SEC_STORE" dsn "" || die "写入 Secret/${SEC_STORE} 失败" "确认 secrets 写权限"
    info "sqlite 模式：建了空 dsn 的 ${SEC_STORE}，只为满足 Deployment 的 secretKeyRef"
  fi

  # 加密主密钥：复用优先，缺失才生成并本地备份；给了不同值一律拒绝（轮换只走 --rotate）。
  local cur=""
  if secret_exists "$SEC_ENC"; then
    cur="$(secret_get_value "$SEC_ENC" key || true)"
  fi
  if [[ -z "$cur" ]]; then
    if [[ -n "$ENC_KEY" ]]; then
      create_secret_from_stdin "$SEC_ENC" key "$ENC_KEY" || die "写入 Secret/${SEC_ENC} 失败" "确认 secrets 写权限"
      ENC_KEY_ORIGIN="按 ENC_KEY 参数写入（未备份，请自行离线保存）"
    else
      ENC_KEY="$(gen_hex32)"
      create_secret_from_stdin "$SEC_ENC" key "$ENC_KEY" || die "写入 Secret/${SEC_ENC} 失败" "确认 secrets 写权限"
      ENC_KEY_BACKUP_PATH="$(backup_key enc-key "$ENC_KEY")"
      ENC_KEY_ORIGIN="本次新生成"
      warn "已生成加密主密钥并在本地留了 600 权限备份：${ENC_KEY_BACKUP_PATH}"
      warn "这把密钥丢失 = 已加密落库的模型 api_key / MCP env / kubeconfig 全部不可恢复，请把备份拷到集群之外"
    fi
  elif [[ -n "$ENC_KEY" && "$ENC_KEY" != "$cur" ]]; then
    die "Secret/${SEC_ENC} 已有一把不同的密钥，脚本绝不覆盖加密主密钥" \
      "确实要换就执行同样的参数加 --rotate（会先备份旧 key，且已加密数据需重新录入）"
  else
    ENC_KEY_ORIGIN="复用集群已有密钥"
    ok "Secret/${SEC_ENC} 已存在，复用（长度 ${#cur}）"
  fi

  if secret_exists "$SEC_ADMIN"; then
    ok "Secret/${SEC_ADMIN} 已存在，沿用既有口令（重跑不轮换口令）"
  else
    local pw
    if [[ -n "$ADMIN_PASSWORD" ]]; then
      pw="$ADMIN_PASSWORD"
      info "首启管理员口令取自 ADMIN_PASSWORD 参数（不在日志里回显）"
    else
      pw="$(gen_password)"; ADMIN_PASSWORD_SHOWN="$pw"
    fi
    create_secret_from_stdin "$SEC_ADMIN" password "$pw" || die "写入 Secret/${SEC_ADMIN} 失败" "确认 secrets 写权限"
    ok "Secret/${SEC_ADMIN} 已创建（仅空库首启用它建内置管理员）"
  fi

  if [[ -n "$LLM_API_KEY" ]]; then
    if secret_exists "$SEC_LLM"; then
      warn "Secret/${SEC_LLM} 已存在，本次不覆盖；要换密钥：kubectl -n ${NAMESPACE} delete secret ${SEC_LLM} 后重跑，或直接在配置中心页面改"
    else
      create_secret_from_stdin "$SEC_LLM" api_key "$LLM_API_KEY" || die "写入 Secret/${SEC_LLM} 失败" "确认 secrets 写权限"
      ok "已播种模型密钥（密钥态：env 恒优先于配置中心页面值）"
    fi
  fi

  if [[ "$BLOBS_BACKEND" == "s3" ]]; then
    if secret_exists "$SEC_BLOBS"; then
      warn "Secret/${SEC_BLOBS} 已存在，沿用既有 AK/SK（不覆盖）"
    else
      create_secret_two_keys "$SEC_BLOBS" access_key "$BLOBS_ACCESS_KEY" secret_key "$BLOBS_SECRET_KEY" ||
        die "写入 Secret/${SEC_BLOBS} 失败" "确认 secrets 写权限"
      ok "Secret/${SEC_BLOBS} 已创建（AK/SK 只进 Secret，不进 ConfigMap）"
    fi
  fi

  if [[ -n "$REGISTRY_SECRET_FROM_FILE" ]]; then
    if secret_exists "k8s-ai-registry"; then
      ok "Secret/k8s-ai-registry 已存在，复用"
    else
      "$KUBECTL_BIN" -n "$NAMESPACE" create secret generic k8s-ai-registry \
        "--from-file=.dockerconfigjson=${REGISTRY_SECRET_FROM_FILE}" \
        --type=kubernetes.io/dockerconfigjson --dry-run=client -o yaml | "$KUBECTL_BIN" apply -f - >/dev/null ||
        die "写入 Secret/k8s-ai-registry 失败" "确认文件是 docker config.json 格式（含 auths 段）"
      ok "私有仓库拉取凭据已写入 Secret/k8s-ai-registry"
    fi
  fi
  stage_done
}

# --- 阶段 4：PVC -------------------------------------------------------------
stage_pvc() {
  stage "阶段 4/9 持久卷"
  if ! is_true "$PERSIST_REPORTS"; then
    warn "PERSIST_REPORTS=false：/reports 用 emptyDir，Pod 重启即清空（仅适合临时验证）"
    stage_done; return 0
  fi
  apply_rendered base/pvc.yaml
  info "等待 PVC Bound（最多 ${WAIT_TIMEOUT}s）"
  if ! "$KUBECTL_BIN" -n "$NAMESPACE" wait --for=condition=bound "persistentvolumeclaim/${PVC_NAME}" --timeout="${WAIT_TIMEOUT}s" >/dev/null 2>&1; then
    _emit "$("$KUBECTL_BIN" -n "$NAMESPACE" describe pvc "$PVC_NAME" 2>&1 | tail -n 25 || true)"
    _emit "$("$KUBECTL_BIN" get storageclass 2>&1 || true)"
    die "PVC ${PVC_NAME} 在 ${WAIT_TIMEOUT}s 内没有 Bound" "看上面的 events：常见是存储类没有 provisioner、容量超限、或访问模式不匹配（多副本要 RWX）"
  fi
  ok "PVC ${PVC_NAME} 已 Bound（${REPORT_STORAGE_SIZE}，存储类 ${STORAGE_CLASS:-集群默认}）"
  stage_done
}

# --- 阶段 5：迁移 Job --------------------------------------------------------
stage_migrate() {
  stage "阶段 5/9 数据库结构迁移"
  if [[ "$STORE_DRIVER" != "mysql" ]]; then
    info "sqlite 模式跳过 Job：server 启动时执行同一套幂等 migrate（库文件在 PVC 上）"
    stage_done; return 0
  fi
  # Job 的 spec 不可变：先删同名旧 Job（只删任务对象，不动数据）。
  "$KUBECTL_BIN" -n "$NAMESPACE" delete job k8s-ai-db-migrate --ignore-not-found >/dev/null 2>&1 || true
  apply_rendered base/db-migrate-job.yaml
  info "等待迁移 Job 完成（最多 ${WAIT_TIMEOUT}s）"
  if ! "$KUBECTL_BIN" -n "$NAMESPACE" wait --for=condition=complete job/k8s-ai-db-migrate --timeout="${WAIT_TIMEOUT}s" >/dev/null 2>&1; then
    _emit "$("$KUBECTL_BIN" -n "$NAMESPACE" logs job/k8s-ai-db-migrate --tail=50 2>&1 || true)"
    die "迁移 Job 未成功完成" "上面是容器日志最后 50 行（多为 DSN 不通/账号无建表权限）；修好后原地重跑本脚本"
  fi
  ok "平台库结构已升级到当前版本"
  stage_done
}

diagnose_rollout() {
  local dep="$1" reasons
  reasons="$("$KUBECTL_BIN" -n "$NAMESPACE" get pods -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.state.waiting.reason}{"\n"}{end}{end}' 2>/dev/null || true)"
  _emit "$("$KUBECTL_BIN" -n "$NAMESPACE" get pods -o wide 2>&1 || true)"
  if printf '%s' "$reasons" | grep -qE 'ImagePullBackOff|ErrImagePull'; then
    die "deployment/${dep} 在 ${WAIT_TIMEOUT}s 内没就绪：镜像拉取失败" \
      "确认 ${IMAGE_REF} 已推送且节点能访问仓库；HTTP 仓库需要 containerd insecure 配置 + IMAGE_PULL_SECRET"
  fi
  _emit "$("$KUBECTL_BIN" -n "$NAMESPACE" get events --sort-by=.lastTimestamp 2>&1 | tail -n 20 || true)"
  die "deployment/${dep} 在 ${WAIT_TIMEOUT}s 内没就绪：容器异常或探针未通过" \
    "kubectl -n ${NAMESPACE} logs deploy/${dep} --tail=80 看启动错误；/readyz 会点明迁移与 Store 条件"
}

# --- 阶段 6：工作负载 --------------------------------------------------------
stage_workloads() {
  stage "阶段 6/9 工作负载（形态 ${FORM}）"
  local deps=(k8s-ai-server) d
  if [[ "$FORM" == "allinone" ]]; then
    apply_rendered allinone/deployment.yaml
  else
    apply_rendered separated/deployment.yaml
    apply_rendered separated/web-deployment.yaml
    deps+=(k8s-ai-web)
  fi
  for d in "${deps[@]}"; do
    info "等待 ${d} 就绪（最多 ${WAIT_TIMEOUT}s）"
    if ! "$KUBECTL_BIN" -n "$NAMESPACE" rollout status "deployment/${d}" --timeout="${WAIT_TIMEOUT}s" >/dev/null 2>&1; then
      diagnose_rollout "$d"
    fi
    ok "${d} 已 Ready"
  done
  stage_done
}

# --- 阶段 7：对外暴露 --------------------------------------------------------
stage_expose() {
  stage "阶段 7/9 对外暴露（${EXPOSE}）"
  apply_rendered base/service.yaml
  if [[ "$FORM" == "separated" ]]; then apply_rendered separated/web-service.yaml; fi

  if [[ "$EXPOSE" == "nodeport" ]]; then
    ACTUAL_NODE_PORT="$("$KUBECTL_BIN" -n "$NAMESPACE" get svc "$ENTRY_SERVICE" -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || true)"
    if [[ -z "$ACTUAL_NODE_PORT" ]]; then
      warn "没读到 ${ENTRY_SERVICE} 的 nodePort（可能还在创建），稍后用：kubectl -n ${NAMESPACE} get svc ${ENTRY_SERVICE}"
    elif [[ -n "$NODE_PORT" ]]; then
      ok "NodePort 固定为 ${ACTUAL_NODE_PORT}（入口 ${ENTRY_SERVICE}）"
    else
      ok "NodePort 由集群随机分配为 ${ACTUAL_NODE_PORT}（想固定就用 --NODE_PORT=）"
    fi
  fi

  if [[ -n "$INGRESS_HOST" ]]; then
    if [[ "$FORM" == "allinone" ]]; then apply_rendered allinone/ingress.yaml; else apply_rendered separated/ingress.yaml; fi
    WEB_URL="http://${INGRESS_HOST}"
    ok "Ingress 就绪：${WEB_URL}（class=${INGRESS_CLASS}）；客户端要能把该域名解析到 Ingress 控制器 IP"
  fi
  stage_done
}

# --- 阶段 8：写权限 RBAC -----------------------------------------------------
stage_write() {
  stage "阶段 8/9 写权限 RBAC"
  if ! is_true "$ENABLE_WRITE"; then
    info "保持只读（默认口径）：Agent 的写操作会被 RBAC 拒绝，读排查不受影响"
    stage_done; return 0
  fi
  _emit "  即将给 ServiceAccount ${NAMESPACE}/k8s-ai 授予集群写权限（delete/patch/apply 等）。"
  _emit "  人工审批闸门仍在应用层，但 RBAC 一放开，误批一次就会真的改动集群。"
  if ! confirm "确认授予写权限 RBAC？"; then
    warn "已跳过写权限授予，保持只读"
    stage_done; return 0
  fi
  apply_rendered rbac-write.example.yaml
  ok "写权限 RBAC 已生效；回收：kubectl delete clusterrolebinding k8s-ai-write && kubectl delete clusterrole k8s-ai-write"
  stage_done
}

# --- 阶段 9：验收摘要 --------------------------------------------------------
stage_summary() {
  stage "阶段 9/9 验收摘要"
  _emit "  访问入口"
  if [[ -n "$WEB_URL" ]]; then _emit "    Ingress  ：${WEB_URL}"; fi
  if [[ -n "$ACTUAL_NODE_PORT" ]]; then
    local ip
    while IFS= read -r ip; do
      [[ -n "$ip" ]] && _emit "    NodePort ：http://${ip}:${ACTUAL_NODE_PORT}"
    done < <(node_addresses)
  fi
  if [[ -z "$WEB_URL" && -z "$ACTUAL_NODE_PORT" ]]; then
    _emit "    集群内   ：http://${ENTRY_SERVICE}.${NAMESPACE}.svc:${ENTRY_PORT}"
  fi
  _emit "    本地转发 ：kubectl -n ${NAMESPACE} port-forward svc/${ENTRY_SERVICE} 18080:${ENTRY_PORT}  然后开 http://127.0.0.1:18080"
  _emit ""
  _emit "  凭据与密钥"
  if [[ -n "$ADMIN_PASSWORD_SHOWN" ]]; then
    _emit "    首启管理员口令（只打印这一次，请立即保存）：${ADMIN_PASSWORD_SHOWN}"
  else
    _emit "    首启管理员口令：复用集群已有 Secret，本次未生成新口令"
  fi
  _emit "    加密主密钥：${ENC_KEY_ORIGIN:-未改动}${ENC_KEY_BACKUP_PATH:+  本地备份=${ENC_KEY_BACKUP_PATH}}"
  _emit ""
  _emit "  自检命令"
  _emit "    kubectl -n ${NAMESPACE} get pods,svc,pvc"
  _emit "    kubectl -n ${NAMESPACE} port-forward svc/${ENTRY_SERVICE} 18080:${ENTRY_PORT} && curl -s localhost:18080/readyz"
  _emit ""
  _emit "  下一步"
  _emit "    1) 打开上面的入口，用管理员账号登录并立刻改掉首启口令"
  _emit "    2) 配置中心填模型 base_url 与模型名；若注入了 LLM_API_KEY（密钥态），页面里的 api_key 会显示「由部署密钥提供」"
  _emit "    3) 集群接入里登记要巡检的集群，发一条会话验证只读工具链"
  _emit "    4) 需要写操作时再跑一次本脚本并加 --ENABLE_WRITE=true（会二次确认）"
  _emit ""
  ok "安装完成。完整日志：${LOG_FILE}"
  stage_done
}

# --- ENC_KEY 轮换（只此一条路径，且必须交互）--------------------------------
run_rotate() {
  CURRENT_STAGE="ENC_KEY 轮换"
  [[ $DRY_RUN -eq 0 ]] || die "--rotate 与 --dry-run 冲突：轮换是写操作" "去掉 --dry-run"
  if [[ $ASSUME_YES -eq 1 || ! -t 0 ]]; then
    die "--rotate 必须交互二次确认，非交互模式直接拒绝" "在交互终端去掉 --yes 重跑"
  fi
  command -v "$KUBECTL_BIN" >/dev/null 2>&1 || die "找不到 kubectl" "先装 kubectl"
  try_kctl --request-timeout=15s get --raw=/version || die "集群不可达" "检查 kubeconfig 与上下文"
  secret_exists "$SEC_ENC" || die "Secret/${SEC_ENC} 不存在，没有可轮换的旧密钥" "先按正常流程装一次"

  local old new
  old="$(secret_get_value "$SEC_ENC" key || true)"
  [[ -n "$old" ]] || die "Secret/${SEC_ENC} 里的 key 是空值，无旧值可备份" "去掉 --rotate，直接重跑安装补上密钥"
  ENC_KEY_BACKUP_PATH="$(backup_key enc-key.old "$old")"
  _emit ""
  _emit "${C_YLW}轮换风险（必须知道再往下走）${C_OFF}"
  _emit "  旧密钥已备份：${ENC_KEY_BACKUP_PATH}（600 权限）"
  _emit "  换密钥后，已加密落库的模型 api_key、MCP env、集群 kubeconfig 全部无法解密。"
  _emit "  当前版本没有自动「导出并重加密」能力，轮换后必须在配置中心重新录入这些值。"
  _emit ""
  if ! confirm "确认轮换 ${NAMESPACE}/${SEC_ENC}？（输入 y 继续，其它任意键放弃）"; then
    warn "已放弃轮换，集群未做任何改动"
    return 0
  fi
  new="$(gen_hex32)"
  local new_backup; new_backup="$(backup_key enc-key.new "$new")"
  create_secret_from_stdin "$SEC_ENC" key "$new" || die "更新 Secret/${SEC_ENC} 失败" "确认 secrets 写权限；旧密钥备份仍在 ${ENC_KEY_BACKUP_PATH}"
  info "滚动重启后端使新密钥生效"
  "$KUBECTL_BIN" -n "$NAMESPACE" rollout restart deployment/k8s-ai-server >/dev/null ||
    die "重启 deployment/k8s-ai-server 失败" "kubectl -n ${NAMESPACE} rollout restart deploy/k8s-ai-server"
  "$KUBECTL_BIN" -n "$NAMESPACE" rollout status deployment/k8s-ai-server --timeout="${WAIT_TIMEOUT}s" >/dev/null ||
    die "滚动重启未在 ${WAIT_TIMEOUT}s 内完成" "kubectl -n ${NAMESPACE} get pods；旧密钥备份仍在 ${ENC_KEY_BACKUP_PATH}，必要时回滚 Secret"
  _emit ""
  ok "轮换完成。新密钥备份：${new_backup}"
  _emit "  必须接着做：登录 → 配置中心重新录入模型 api_key / MCP env / 集群凭据，然后逐个「测试连接」验证。"
  _emit "  旧密钥备份请保留到确认所有敏感值都已重录之后：${ENC_KEY_BACKUP_PATH}"
}

# --- 主流程 ------------------------------------------------------------------
main() {
  parse_args "$@"
  # 全程 tee：失败也留完整现场。放在参数解析之后，--help 不会留下空日志文件。
  if [[ "${K8S_AI_INSTALL_NO_TEE:-0}" != "1" ]]; then
    exec > >(tee -a "$LOG_FILE") 2>&1
  fi
  validate_config
  compute_derived
  if [[ $DO_ROTATE -eq 1 ]]; then run_rotate; return 0; fi
  banner
  if [[ $DRY_RUN -eq 1 ]]; then
    CURRENT_STAGE="清单渲染预演"
    stage "预演：渲染清单 + 占位符断言（不接触集群）"
    render_all
    local rel
    for rel in "${RENDER_LIST[@]}"; do _emit "    ${rel}"; done
    KEEP_RENDER=1
    ok "预演通过：占位符零残留。清单在 ${RENDER_DIR}，可逐个 kubectl apply --dry-run=client -f 复核"
    _emit "  注意：--dry-run 不校验集群权限、存储类与端口占用，真装前建议再跑一次 --diff。"
    stage_done
    return 0
  fi
  if [[ $DO_DIFF -eq 1 ]]; then run_diff; return 0; fi

  stage_build
  stage_preflight
  stage_base
  stage_secrets
  stage_pvc
  stage_migrate
  stage_workloads
  stage_expose
  stage_write
  stage_summary
}

# --- 派生值 ------------------------------------------------------------------
compute_derived() {
  IMAGE_REF="${IMAGE}:${TAG}"
  WEB_IMAGE_REF=""
  if [[ -n "$IMAGE_WEB" ]]; then
    # 已经带标签的地址照用，没带就沿用同一个 TAG，前后端版本不会错配。
    case "$IMAGE_WEB" in
      *:*) WEB_IMAGE_REF="$IMAGE_WEB" ;;
      *) WEB_IMAGE_REF="${IMAGE_WEB}:${TAG}" ;;
    esac
  fi

  DATA_ON_PVC=false
  if [[ "$STORE_DRIVER" == "sqlite" ]]; then DATA_ON_PVC=true; fi

  EXPOSED_SVC="__none__"
  ENTRY_SERVICE="k8s-ai-server"
  ENTRY_PORT=8080
  if [[ "$EXPOSE" == "nodeport" ]]; then
    if [[ "$FORM" == "allinone" ]]; then
      EXPOSED_SVC="service.yaml"; ENTRY_SERVICE="k8s-ai-server"; ENTRY_PORT=8080
    else
      # 形态 B 的对外入口是前端 Nginx：/api/v1 由它在集群内转发给后端。
      EXPOSED_SVC="web-service.yaml"; ENTRY_SERVICE="k8s-ai-web"; ENTRY_PORT=80
    fi
  fi

  PULLED_SECRET_NAME=""
  REGISTRY_SECRET_FROM_FILE=""
  if [[ -n "$IMAGE_PULL_SECRET" ]]; then
    if [[ -f "$IMAGE_PULL_SECRET" ]]; then
      REGISTRY_SECRET_FROM_FILE="$IMAGE_PULL_SECRET"
      PULLED_SECRET_NAME="k8s-ai-registry"
    else
      PULLED_SECRET_NAME="$IMAGE_PULL_SECRET"
    fi
  fi

  if [[ -z "$GOPROXY" && -f "${HOME}/.k8s-ai/goproxy.txt" ]]; then
    GOPROXY="$(head -n1 "${HOME}/.k8s-ai/goproxy.txt" | tr -d ' \r\n')"
    [[ -n "$GOPROXY" ]] && detail "GOPROXY 取自 ~/.k8s-ai/goproxy.txt"
  fi
  [[ -n "$GOPROXY" ]] || GOPROXY="https://proxy.golang.org,direct"

  GIT_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo none)"
}

banner() {
  _emit ""
  _emit "${C_BLU}k8s-ai 四期一键安装${C_OFF}  形态=${FORM}  命名空间=${NAMESPACE}  存储=${STORE_DRIVER}"
  _emit "  镜像      ：${IMAGE_REF}${WEB_IMAGE_REF:+ / ${WEB_IMAGE_REF}}   拉取策略=${IMAGE_PULL_POLICY}"
  _emit "  暴露方式  ：${EXPOSE}${INGRESS_HOST:+ + ingress(${INGRESS_HOST})}${NODE_PORT:+  nodePort=${NODE_PORT}(空=随机)}"
  _emit "  持久化    ：PVC=${PERSIST_REPORTS} 容量=${REPORT_STORAGE_SIZE} 存储类=${STORAGE_CLASS:-<集群默认>}"
  _emit "  对象存储  ：${BLOBS_BACKEND}${BLOBS_BUCKET:+ bucket=${BLOBS_BUCKET}}   写权限 RBAC=${ENABLE_WRITE}"
  _emit "  日志      ：${LOG_FILE}"
  if [[ -n "$ENV_FILE_PATH" ]]; then _emit "  参数文件  ：${ENV_FILE_PATH}（CLI > 环境变量 > 文件 > 内置默认）"; fi
}

# --- BUILD 段 ----------------------------------------------------------------
stage_build() {
  stage "阶段 0/9 镜像构建（BUILD=${BUILD}）"
  if [[ -n "$LOAD_TAR" ]]; then
    [[ -f "$LOAD_TAR" ]] || die "--load-tar 文件不存在：${LOAD_TAR}" "确认离线包路径（docker save 产出的 tar）"
    pick_container_tool || die "离线导入需要 docker/podman 才能 load 镜像" "在没有运行时的机器上，请把 tar 拷到有 docker 的机器执行本脚本"
    info "${CONTAINER_TOOL} load -i ${LOAD_TAR}"
    "$CONTAINER_TOOL" load -i "$LOAD_TAR" || die "docker load 失败" "确认 tar 完整（tar tf 自检）后重跑"
    if ! "$CONTAINER_TOOL" image inspect "$IMAGE_REF" >/dev/null 2>&1; then
      die "离线包里没有 ${IMAGE_REF}" "先 docker tag <包内镜像> ${IMAGE_REF}，或改 TAG 对齐后重跑"
    fi
  elif ! is_true "$BUILD"; then
    info "BUILD=false：跳过构建，直接发布已就绪的 ${IMAGE_REF}"
  elif ! pick_container_tool; then
    warn "没探测到 docker/podman，降级为「只发布不构建」；请确认 ${IMAGE_REF} 已推到仓库"
    stage_done; return 0
  else
    info "${CONTAINER_TOOL} build（GOPROXY=${GOPROXY} KUBECTL_VERSION=${KUBECTL_VERSION:-<不内置>}）"
    local args=(build -f "${REPO_ROOT}/Dockerfile" --build-arg "VERSION=${TAG}" --build-arg "COMMIT=${GIT_COMMIT}"
                --build-arg "GOPROXY=${GOPROXY}" --build-arg "KUBECTL_VERSION=${KUBECTL_VERSION}"
                --build-arg "KUBECTL_DOWNLOAD_BASE=${KUBECTL_DOWNLOAD_BASE}" -t "$IMAGE_REF")
    if [[ $VERBOSE -eq 1 ]]; then args+=(--progress=plain); fi
    detail "\$ ${CONTAINER_TOOL} ${args[*]} ${REPO_ROOT}"
    ( cd "$REPO_ROOT" && "${CONTAINER_TOOL}" "${args[@]}" "$REPO_ROOT" ) ||
      die "后端镜像构建失败" "内网构建请带 --KUBECTL_DOWNLOAD_BASE=<内网mirror>（目录结构同 dl.k8s.io/release），或先把 kubectl 放进 deploy/kubectl/ 走离线投递"
    if [[ "$FORM" == "separated" ]]; then
      info "构建前端镜像 ${WEB_IMAGE_REF}"
      ( cd "$REPO_ROOT" && "${CONTAINER_TOOL}" build -f "${SCRIPT_DIR}/separated/web.Dockerfile" \
          --build-arg "VERSION=${TAG}" --build-arg "COMMIT=${GIT_COMMIT}" -t "$WEB_IMAGE_REF" "$REPO_ROOT" ) ||
        die "前端镜像构建失败" "看 npm ci / npm run build 的报错；离线环境需要可达的 npm registry"
    fi
  fi

  if is_true "$PUSH"; then
    if ! pick_container_tool; then
      warn "没有容器运行时，无法推送；请自行把 ${IMAGE_REF} 送进仓库"
    else
      info "推送 ${IMAGE_REF}"
      "$CONTAINER_TOOL" push "$IMAGE_REF" || die "推送 ${IMAGE_REF} 失败" "确认仓库地址/凭据；HTTP 仓库需要 IMAGE_PULL_SECRET 与 containerd insecure 配置"
      if [[ "$FORM" == "separated" && -n "$WEB_IMAGE_REF" ]]; then
        info "推送 ${WEB_IMAGE_REF}"
        "$CONTAINER_TOOL" push "$WEB_IMAGE_REF" || die "推送 ${WEB_IMAGE_REF} 失败" "同上"
      fi
    fi
  else
    info "PUSH=false：镜像只在本地，集群若拉不到请把镜像导入节点或改 PUSH=true"
  fi
  stage_done
}

# --- 阶段 1：前置检查 --------------------------------------------------------
can_i() { # $1=verb $2=resource $3=extra(如 -n ns)
  if [[ -n "$3" ]]; then try_kctl auth can-i "$1" "$2" $3; else try_kctl auth can-i "$1" "$2"; fi
}

stage_preflight() {
  stage "阶段 1/9 前置检查"
  command -v "$KUBECTL_BIN" >/dev/null 2>&1 ||
    die "找不到 kubectl" "安装 kubectl 或用 KUBECTL_BIN=/path/to/kubectl 指定路径"
  try_kctl --request-timeout=15s get --raw=/version ||
    die "集群不可达或未配置凭据" "kubectl config current-context 检查上下文；离线环境确认 kubeconfig 存在"
  ok "kubectl 可用，集群可达：$("$KUBECTL_BIN" config current-context 2>/dev/null || echo '<未知上下文>')"

  if ! try_kctl get namespace "$NAMESPACE"; then
    can_i create namespace "" ||
      die "命名空间 ${NAMESPACE} 不存在，且当前身份没有创建命名空间的权限" \
        "让管理员先 kubectl create namespace ${NAMESPACE}，或换成有权限的 kubeconfig"
    warn "命名空间 ${NAMESPACE} 将由本脚本创建"
  else
    ok "命名空间 ${NAMESPACE} 已存在（重跑不会重置任何密钥）"
  fi

  # 逐项 auth can-i：把权限问题一次性摊开，而不是等到 apply 阶段逐条撞墙。
  local missing=() item verb rest res scope
  for item in "create|deployments.apps|-n ${NAMESPACE}" \
              "create|jobs.batch|-n ${NAMESPACE}" \
              "create|configmaps|-n ${NAMESPACE}" \
              "create|secrets|-n ${NAMESPACE}" \
              "create|services|-n ${NAMESPACE}" \
              "create|serviceaccounts|-n ${NAMESPACE}" \
              "create|clusterroles|" \
              "create|clusterrolebindings|"; do
    verb="${item%%|*}"; rest="${item#*|}"; res="${rest%%|*}"; scope="${rest#*|}"
    if ! can_i "$verb" "$res" "$scope"; then missing+=("${verb} ${res}${scope:+ ${scope}}"); fi
  done
  if is_true "$PERSIST_REPORTS"; then
    can_i create persistentvolumeclaims "-n ${NAMESPACE}" || missing+=("create persistentvolumeclaims -n ${NAMESPACE}")
  fi
  if [[ -n "$INGRESS_HOST" ]]; then
    can_i create ingresses.networking.k8s.io "-n ${NAMESPACE}" || missing+=("create ingresses -n ${NAMESPACE}")
  fi
  if [[ ${#missing[@]} -gt 0 ]]; then
    die "当前身份缺少集群权限：${missing[*]}" \
      "让管理员补授这些权限，或换有权限的 kubeconfig；只想看渲染结果就用 --dry-run"
  fi
  ok "权限自检通过（部署所需的 create/patch 权限齐备）"

  # 必需 API 组
  local apis def used
  apis="$("$KUBECTL_BIN" api-versions 2>/dev/null || true)"
  case "$apis" in *"batch/v1"*) ;; *) die "集群没有 batch/v1 API，迁移 Job 无法运行" "确认集群版本 ≥ 1.21" ;; esac
  if [[ -n "$INGRESS_HOST" ]]; then
    case "$apis" in *"networking.k8s.io/v1"*) ;; *) die "集群没有 networking.k8s.io/v1，无法创建 Ingress" "改用 --EXPOSE=nodeport 或升级集群" ;; esac
    try_kctl get ingressclass "$INGRESS_CLASS" ||
      die "IngressClass ${INGRESS_CLASS} 不存在" "kubectl get ingressclass 看现场类名，用 --INGRESS_CLASS 指定"
    ok "IngressClass ${INGRESS_CLASS} 就绪"
  fi

  # 存储类：给了名字就查名字，没给就要求集群有默认存储类
  if is_true "$PERSIST_REPORTS"; then
    if [[ -n "$STORAGE_CLASS" ]]; then
      try_kctl get storageclass "$STORAGE_CLASS" ||
        die "StorageClass ${STORAGE_CLASS} 不存在" "kubectl get sc 看真实名字（注意带 (default) 后缀）"
      ok "StorageClass ${STORAGE_CLASS} 就绪"
    else
      def="$("$KUBECTL_BIN" get sc -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{" "}{end}' 2>/dev/null || true)"
      if [[ -z "${def// /}" ]]; then
        _emit "$("$KUBECTL_BIN" get sc 2>/dev/null || true)"
        die "集群没有默认 StorageClass，且没指定 STORAGE_CLASS" \
          "从上面挑一个用 --STORAGE_CLASS=<名字>，或把 PVC 关掉 --PERSIST_REPORTS=false（重启即丢，仅限临时验证）"
      fi
      ok "使用集群默认存储类：${def}"
    fi
  fi

  # NodePort 占用
  if [[ -n "$NODE_PORT" && "$EXPOSED_SVC" != "__none__" ]]; then
    used="$("$KUBECTL_BIN" get svc -A -o jsonpath='{range .items[*]}{range .spec.ports[*]}{.nodePort}{"\n"}{end}{end}' 2>/dev/null | tr -d 'null' || true)"
    if printf '%s\n' "$used" | grep -qx "$NODE_PORT"; then
      die "NodePort ${NODE_PORT} 已被占用" "换一个端口，或留空由集群随机分配"
    fi
    ok "NodePort ${NODE_PORT} 空闲"
  fi
  stage_done
}

# 入口必须放在所有函数定义之后：bash 是按行执行的，提前调用会找不到后面定义的函数。
main "$@"
