FROM registry.cn-hangzhou.aliyuncs.com/bodesi/node:24.12 AS frontend-builder
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --registry=https://registry.npmmirror.com
COPY frontend/index.html frontend/tsconfig.json frontend/vite.config.ts ./
COPY frontend/public ./public
COPY frontend/src ./src
RUN npm run build

FROM registry.cn-hangzhou.aliyuncs.com/bodesi/golang:1.25 AS builder
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG IMAGE_TAG=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_TAG}" -o /out/confhub ./cmd/main
FROM docker.m.daocloud.io/alpine:3.23
WORKDIR /app
RUN apk add --no-cache ca-certificates && \
    addgroup --system confhub && \
    adduser --system --disabled-password --no-create-home --uid 10001 --ingroup confhub confhub
COPY --from=builder --chown=confhub:confhub /out/confhub ./confhub
COPY --from=frontend-builder --chown=confhub:confhub /frontend/dist ./frontend
USER confhub:confhub
EXPOSE 8080
ENTRYPOINT ["/app/confhub"]
