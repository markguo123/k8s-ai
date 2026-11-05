# syntax=docker/dockerfile:1
# k8s-ai 前端静态镜像（P6.2 / P6.13 形态 B）：Nginx + web/dist。
#
# 与仓库根 Dockerfile 是两个互不依赖的独立文件（D-30 / ADR-041）：
#   仓库根 Dockerfile      后端镜像（形态 A 内嵌 dist；形态 B 关掉静态托管复用同一镜像）
#   deploy/separated/web.Dockerfile  前端静态镜像（形态 B 专用）
# 两者可分别构建、发布、回滚。形态 A 不需要构建本镜像。
#
# 构建：
#   docker build -f deploy/separated/web.Dockerfile --build-arg VERSION=4.0.0 -t k8s-ai-web:4.0.0 .
# 离线/内网加速（跳过 npm）：用一个「目录根下含 dist/」的目录覆盖 webdist 阶段：
#   docker build -f deploy/separated/web.Dockerfile --build-context webdist=<dir-containing-dist> -t k8s-ai-web:dev .
#
# 运行镜像选 nginx-unprivileged：非 root 运行、监听 8080，直接满足 Pod Security
# restricted（见 deploy/separated/web-deployment.yaml 的端口说明）。

FROM node:22-alpine AS webdist
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
# 产物归一到阶段根目录的 /dist，与 --build-context 覆盖时的路径约定一致。
RUN rm -rf /dist && mkdir -p /dist && cp -r /web/dist/. /dist/

FROM nginxinc/nginx-unprivileged:1.27-alpine
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="k8s-ai-web" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
# 站点配置整体替换默认 default.conf（root 仍是镜像内的 /usr/share/nginx/html）。
COPY deploy/separated/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=webdist /dist /usr/share/nginx/html
EXPOSE 8080
# 前端固定使用相对路径 /api/v1（无构建期开关），因此形态 B 必须同域按路径分流，
# 或保留 deploy/separated/nginx.conf 里的 API 反代段；跨域方案本期不提供。
