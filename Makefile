BINARY := bin/confhub
# Explicit packages avoid walking unreadable database bind mounts under data/.
SERVER_PACKAGES := ./cmd/... ./internal/...
PACKAGE := ./cmd/main
DOCKER_REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/bodesi/confhub
IMAGE_TAG ?= dev
SDK_PYTHON ?= sdk/python/.venv/bin/python

go-mod-download:
	go mod download

test: go-mod-download
	go test $(SERVER_PACKAGES)

test-unit:
	go test -count=1 ./internal/config ./internal/settings
	$(MAKE) sdk-unit

sdk-unit:
	go -C sdk/go test -race -short -count=1 ./...
	$(SDK_PYTHON) -m unittest discover -s sdk/python/tests -p test_client.py -v

# Disposable PostgreSQL and three real instances; no deployment data is used.
test-full:
	python3 tests/run.py --python $(abspath $(SDK_PYTHON)) --coverage-gates

# Public API/SDK tests against supplied running instances; credentials are env-only.
test-system:
	go -C tests/system run -race .

test-fuzz:
	CGO_ENABLED=1 go test ./internal/config -run '^$$' -fuzz '^FuzzIPSingletonRange$$' -fuzztime=5s -parallel=2
	CGO_ENABLED=1 go test ./internal/config -run '^$$' -fuzz '^FuzzNamesAndTextValidation$$' -fuzztime=5s -parallel=2

test-integration:
	@test -n "$(CONFHUB_TEST_POSTGRES_DSN)" || (echo "CONFHUB_TEST_POSTGRES_DSN is required"; exit 1)
	go test -count=1 ./internal/storage ./internal/syncer ./internal/server

test-race:
	@test -n "$(CONFHUB_TEST_POSTGRES_DSN)" || (echo "CONFHUB_TEST_POSTGRES_DSN is required"; exit 1)
	CGO_ENABLED=1 go test -race -count=1 $(SERVER_PACKAGES)

vet:
	go vet $(SERVER_PACKAGES)

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
	$(SDK_PYTHON) -m ruff check --config sdk/python/pyproject.toml tests
	go -C tests/system vet ./...

sdk-integration:
	python3 sdk/test-integration.py --python $(abspath $(SDK_PYTHON))

clean:
	rm -rf bin

docker-image-build-local:
	docker build --platform=linux/amd64 \
		--build-arg IMAGE_TAG=$(IMAGE_TAG) \
		-t $(DOCKER_IMAGE):$(IMAGE_TAG) .

.PHONY: go-mod-download test test-unit test-integration test-race vet build run clean docker-image-build-local frontend-install frontend-build frontend-test frontend-check frontend-e2e sdk-test sdk-check sdk-integration sdk-unit test-full test-system test-fuzz
