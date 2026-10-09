BINARY := bin/confhub
PACKAGE := ./cmd/main
DOCKER_REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/bodesi/confhub
IMAGE_TAG ?= dev

go-mod-download:
	go mod download

test: go-mod-download
	go test ./...

test-unit:
	go test ./internal/config

test-integration:
	@test -n "$(CONFHUB_TEST_POSTGRES_DSN)" || (echo "CONFHUB_TEST_POSTGRES_DSN is required"; exit 1)
	go test -count=1 ./internal/storage ./internal/syncer ./internal/server

test-race:
	@test -n "$(CONFHUB_TEST_POSTGRES_DSN)" || (echo "CONFHUB_TEST_POSTGRES_DSN is required"; exit 1)
	go test -race -count=1 ./...

vet:
	go vet ./...

build: go-mod-download
	@mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) $(PACKAGE)

run:
	go run $(PACKAGE)

clean:
	rm -rf bin

docker-image-build-local:
	docker build --platform=linux/amd64 \
		--build-arg IMAGE_TAG=$(IMAGE_TAG) \
		-t $(DOCKER_IMAGE):$(IMAGE_TAG) .

.PHONY: go-mod-download test test-unit test-integration test-race vet build run clean docker-image-build-local
