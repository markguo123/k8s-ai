# syntax=docker/dockerfile:1
# k8s-ai 后端镜像（P6.11 / D-30 形态 A）：内嵌前端产物，单端口同时提供 SPA 与 API。
#
# 默认构建路径可复现：前端在 node 阶段按 package-lock.json 干净安装后构建，
# 不依赖开发者本地产物；仓库里的 web/dist 只用于让 go:embed 在干净检出下编译。
#
# 构建示例：
#   docker build --build-arg VERSION=4.0.0 --build-arg COMMIT=<sha> -t k8s-ai:4.0.0 .
# 内网构建（kubectl 走内网 mirror，目录结构需与 dl.k8s.io/release 一致）：
#   docker build --build-arg KUBECTL_DOWNLOAD_BASE=http://mirror.internal/k8s-release \
#                --build-arg KUBECTL_VERSION=v1.29.15 -t k8s-ai:4.0.0 .
# 构建断言（P6.3 §8.3，交付前自查）：
#   docker run --rm --entrypoint /bin/bash <img> -lc 'id -u; command -v bash jq kubectl; kubectl version --client'
#   期望 uid=65532，三个命令都可用。
#
# 运行阶段为什么带 shell 与 kubectl（D-33 / ADR-044 v1.1）：容器化后平台能力不能比本地
# CLI 少，管道/统计类排障命令是现场定位问题的主要手段。安全不靠"镜像里没有解释器"，
# 靠既有闸门：含 shell 构造的命令一律经真人审批且不被 --skip-permissions 免除、写操作经
# 人工审批、Secret 值与 --raw 硬拒、真实权限由集群 RBAC 决定。可用 platform.exec.shell_enabled
# 关闭 shell 通道；安全基线明确禁止容器内 shell 的现场改用 Dockerfile.distroless。
# 离线加速（可选）：用一个只含 dist/ 的目录覆盖 webdist 阶段，跳过 npm 构建。
#   docker build --build-context webdist=<dir-containing-dist> -t k8s-ai:dev .
# 形态 B（纯 API，前端由独立 Nginx 镜像提供）：同一镜像，部署时把
#   platform.server.static_enabled 设为 false 即可。

# ---- BUILD STAGES BEGIN ----
# 本段在两档镜像（Dockerfile / Dockerfile.distroless）里必须逐字一致（tests/arch 断言）：
# 两档镜像只允许在运行阶段分叉，构建出的二进制与前端产物必须完全相同。
FROM node:22-alpine AS webdist
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
# 产物归一到阶段根目录的 /dist：与 --build-context 覆盖时「目录根下 dist/」的
# 路径约定一致，两条构建路径共用同一条 COPY。
RUN rm -rf /dist && mkdir -p /dist && cp -r /web/dist/. /dist/

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 先清掉可能随上下文带进来的本地产物，保证镜像里的 dist 只来自构建阶段。
RUN rm -rf ./web/dist
COPY --from=webdist /dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
      -X github.com/k8s-ai/k8s-ai/internal/version.Version=${VERSION} \
      -X github.com/k8s-ai/k8s-ai/internal/version.Commit=${COMMIT} \
      -X github.com/k8s-ai/k8s-ai/internal/version.Date=${DATE}" \
    -o /out/k8s-ai ./cmd/k8s-ai
# ---- BUILD STAGES END ----

# 运行阶段（P6.3）：debian slim + 排障工具层。base 里 bash/coreutils/grep/sed/awk/diff
# 本就有，显式列出来是为了让"这档镜像能跑哪些命令"在文件里可读，不靠镜像血缘巧合。
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash coreutils grep sed mawk diffutils \
        jq curl ca-certificates \
        dnsutils iputils-ping less \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# kubectl 在构建阶段下载并做 sha256 校验（运行期不联网、不下载）。
#   KUBECTL_VERSION：按现场集群次版本调整（kubectl 官方支持 ±1 次版本偏差）；
#     置空 = 本镜像不内置 kubectl，kubectl 工具会拿到"kubectl not found"可指导文案，
#     只读链路（client-go 结构化工具与 scan）不受影响。
#   KUBECTL_DOWNLOAD_BASE：内网 mirror 根路径，目录结构需与 dl.k8s.io/release 一致。
#   TARGETARCH 由 BuildKit 注入，跨架构构建时自动取对应二进制。
ARG KUBECTL_VERSION=v1.29.15
ARG KUBECTL_DOWNLOAD_BASE=https://dl.k8s.io/release
ARG TARGETARCH
RUN set -eu; \
    if [ -z "${KUBECTL_VERSION}" ]; then \
        echo "KUBECTL_VERSION 为空：跳过 kubectl 内置（保留 shell 工具层）"; \
        exit 0; \
    fi; \
    arch="${TARGETARCH:-amd64}"; \
    base="${KUBECTL_DOWNLOAD_BASE}/${KUBECTL_VERSION}/bin/linux/${arch}"; \
    curl -fsSLo /usr/local/bin/kubectl "${base}/kubectl"; \
    curl -fsSLo /tmp/kubectl.sha256 "${base}/kubectl.sha256"; \
    printf '%s  %s\n' "$(cat /tmp/kubectl.sha256)" /usr/local/bin/kubectl | sha256sum -c -; \
    chmod 0755 /usr/local/bin/kubectl; \
    rm -f /tmp/kubectl.sha256

# 非 root 运行：uid/gid 65532 与 deploy 清单的 runAsUser 一致（tests/arch 交叉断言）。
# HOME 必须可写：kubectl 把服务发现缓存写在 $HOME/.kube，而清单开了只读根文件系统，
# 所以 deploy 侧给 /home/k8sai 挂了 emptyDir，这里同时把 HOME 指过去。
RUN groupadd --gid 65532 k8sai \
    && useradd --uid 65532 --gid 65532 --create-home --home-dir /home/k8sai --shell /bin/bash k8sai

COPY --from=build /out/k8s-ai /usr/local/bin/k8s-ai
ENV HOME=/home/k8sai
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/k8s-ai"]
