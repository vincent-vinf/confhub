#!/usr/bin/env bash
# Owns both containers; never uses deployment databases or real configuration.
set -euo pipefail
cd "$(dirname "$0")/.."
run_id="confhub-interop-$(date +%s)-$$"
pg_name="$run_id-pg"
nacos_name="$run_id-nacos"
cleanup() {
  docker rm -f "$nacos_name" "$pg_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
pg_password=$(openssl rand -hex 16)
docker run -d --name "$pg_name" -e POSTGRES_PASSWORD="$pg_password" \
  -p 127.0.0.1::5432 registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17 >/dev/null
docker run -d --name "$nacos_name" -e MODE=standalone -e NACOS_AUTH_ENABLE=false \
  -e JVM_XMS=256m -e JVM_XMX=512m -p 127.0.0.1::8848 nacos/nacos-server:v2.5.1 >/dev/null
pg_port=$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$pg_name")
nacos_port=$(docker inspect -f '{{(index (index .NetworkSettings.Ports "8848/tcp") 0).HostPort}}' "$nacos_name")
export CONFHUB_TEST_POSTGRES_DSN="postgres://postgres:$pg_password@127.0.0.1:$pg_port/postgres?sslmode=disable"
export CONFHUB_TEST_NACOS_URL="http://127.0.0.1:$nacos_port"
ready=false
for ((attempt=0; attempt<120; attempt++)); do
  if docker exec "$pg_name" pg_isready -U postgres >/dev/null 2>&1 && \
    curl -fsS --max-time 1 "$CONFHUB_TEST_NACOS_URL/nacos/v1/console/health/readiness" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo 'Disposable PostgreSQL/Nacos did not become ready.' >&2
  exit 1
fi
go test -count=1 -v ./internal/server -run '^TestRealNacosNewZIPInteroperability$'
