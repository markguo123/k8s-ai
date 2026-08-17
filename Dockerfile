# k8s-ai 多阶段构建：非 root、小镜像（distroless static），prompts 已 go:embed 进二进制。
# 构建示例：
#   docker build --build-arg VERSION=0.1.0 -t k8s-ai:0.1.0 .
# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
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