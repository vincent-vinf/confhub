#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
test_name="confhub-ui-test-$$"
test_tmp=$(mktemp -d)
cleanup() {
  if [[ -n "${test_server_pid:-}" ]]; then kill "$test_server_pid" 2>/dev/null || true; fi
  docker rm -f "$test_name" >/dev/null 2>&1 || true
  rm -rf "$test_tmp"
}
trap cleanup EXIT INT TERM
docker run -d --name "$test_name" -p 127.0.0.1::5432 \
  -e POSTGRES_DB=confhub -e POSTGRES_USER=confhub -e POSTGRES_PASSWORD=ui-test-password \
  registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17 >/dev/null
test_port=$(docker port "$test_name" 5432/tcp | sed 's/.*://')
for attempt in {1..60}; do
  if docker exec "$test_name" pg_isready -U confhub -d confhub >/dev/null 2>&1; then break; fi
  sleep 0.5
done
npm --prefix frontend run build
go build -o "$test_tmp/confhub" ./cmd/main
CONFHUB_DSN="postgres://confhub:ui-test-password@127.0.0.1:$test_port/confhub?sslmode=disable" \
CONFHUB_ADMIN_PASSWORD=ui-test-password \
CONFHUB_JWT_SECRET=ui-test-only-secret-at-least-32-bytes \
CONFHUB_STATIC_DIR="$PWD/frontend/dist" \
CONFHUB_LISTEN=127.0.0.1:18080 "$test_tmp/confhub" &
test_server_pid=$!
wait "$test_server_pid"
