# syntax=docker/dockerfile:1
# k8s-ai 后端镜像（P6.11 / D-30 形态 A）：内嵌前端产物，单端口同时提供 SPA 与 API。
#
# 默认构建路径可复现：前端在 node 阶段按 package-lock.json 干净安装后构建，
# 不依赖开发者本地产物；仓库里的 web/dist 只用于让 go:embed 在干净检出下编译。
#
# 构建示例：
#   docker build --build-arg VERSION=4.0.0 --build-arg COMMIT=<sha> -t k8s-ai:4.0.0 .
# 离线加速（可选）：用一个只含 dist/ 的目录覆盖 webdist 阶段，跳过 npm 构建。
#   docker build --build-context webdist=<dir-containing-dist> -t k8s-ai:dev .
# 形态 B（纯 API，前端由独立 Nginx 镜像提供）：同一镜像，部署时把
#   platform.server.static_enabled 设为 false 即可。

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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/k8s-ai /usr/local/bin/k8s-ai
ENTRYPOINT ["/usr/local/bin/k8s-ai"]
