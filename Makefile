BINARY := bin/confhub
PACKAGE := ./cmd/main
DOCKER_REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/bodesi/confhub
IMAGE_TAG ?= dev
SDK_PYTHON ?= sdk/python/.venv/bin/python

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
	CGO_ENABLED=1 go test -race -count=1 ./...

vet:
	go vet ./...

build: go-mod-download
	@mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) $(PACKAGE)

run:
	go run $(PACKAGE)

frontend-install:
	npm --prefix frontend ci

frontend-build:
	npm --prefix frontend run build

frontend-test:
	npm --prefix frontend test

frontend-check:
	npm --prefix frontend run typecheck
	npm --prefix frontend test

# Requires Docker and a Playwright Chromium installation. Uses a disposable DB.
frontend-e2e:
	npm --prefix frontend run test:e2e

sdk-test:
	go -C sdk/go test -race ./...
	$(SDK_PYTHON) -m unittest discover -s sdk/python/tests -v

sdk-check:
	go -C sdk/go vet ./...
	$(SDK_PYTHON) -m mypy --config-file sdk/python/pyproject.toml sdk/python/src/confhub
	$(SDK_PYTHON) -m ruff check --config sdk/python/pyproject.toml sdk/python/src sdk/python/tests sdk/test-integration.py

sdk-integration:
	python3 sdk/test-integration.py --python $(abspath $(SDK_PYTHON))

clean:
	rm -rf bin

docker-image-build-local:
	docker build --platform=linux/amd64 \
		--build-arg IMAGE_TAG=$(IMAGE_TAG) \
		-t $(DOCKER_IMAGE):$(IMAGE_TAG) .

.PHONY: go-mod-download test test-unit test-integration test-race vet build run clean docker-image-build-local frontend-install frontend-build frontend-test frontend-check frontend-e2e sdk-test sdk-check sdk-integration
