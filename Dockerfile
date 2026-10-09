FROM registry.cn-hangzhou.aliyuncs.com/bodesi/golang:1.25 AS builder
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY frontend ./frontend
ARG IMAGE_TAG=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_TAG}" -o /out/confhub ./cmd/main
# React assets are files on disk. Backend-only builds have an empty directory;
# build the future UI into frontend/dist before building its application image.
RUN mkdir -p /out/frontend && if [ -d frontend/dist ]; then cp -a frontend/dist/. /out/frontend/; fi
FROM docker.m.daocloud.io/alpine:3.23
WORKDIR /app
RUN apk add --no-cache ca-certificates && \
    addgroup --system confhub && \
    adduser --system --disabled-password --no-create-home --uid 10001 --ingroup confhub confhub
COPY --from=builder --chown=confhub:confhub /out/confhub ./confhub
COPY --from=builder --chown=confhub:confhub /out/frontend ./frontend
USER confhub:confhub
EXPOSE 8080
ENTRYPOINT ["/app/confhub"]
