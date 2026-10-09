FROM registry.cn-hangzhou.aliyuncs.com/bodesi/golang:1.25 AS builder

WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

ARG IMAGE_TAG=dev
COPY cmd ./cmd

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=${IMAGE_TAG}" \
    -o /out/confhub ./cmd/main

FROM docker.m.daocloud.io/alpine:3.23

WORKDIR /app
RUN apk add --no-cache ca-certificates && \
    addgroup --system confhub && \
    adduser --system --disabled-password --no-create-home --uid 10001 --ingroup confhub confhub

COPY --from=builder --chown=confhub:confhub /out/confhub ./confhub

USER confhub:confhub
EXPOSE 8080

ENTRYPOINT ["/app/confhub"]
