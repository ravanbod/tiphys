#!/usr/bin/env bash
# Throwaway PostgreSQL for the postgres adapter tests.
#
#   eval "$(deploy/sql/test-db.sh start)"   # exports TIPHYS_TEST_DATABASE_URL
#   go test ./internal/infra/postgres/...
#   deploy/sql/test-db.sh stop
#
# The container publishes a random free port on 127.0.0.1 and is removed on stop.
set -euo pipefail

NAME="${TIPHYS_TEST_DB_CONTAINER:-tiphys-test-db}"
IMAGE="${TIPHYS_TEST_DB_IMAGE:-postgres:16-alpine}"
PASSWORD="${TIPHYS_TEST_DB_PASSWORD:-tiphys-test}"

case "${1:-}" in
start)
  if ! docker inspect "$NAME" >/dev/null 2>&1; then
    docker run -d --rm --name "$NAME" -p 127.0.0.1::5432 \
      -e POSTGRES_PASSWORD="$PASSWORD" "$IMAGE" >/dev/null
  fi
  for _ in $(seq 1 60); do
    # pg_isready can answer during the init-time restart, so require a real query.
    if docker exec "$NAME" psql -U postgres -tAc 'select 1' >/dev/null 2>&1; then break; fi
    sleep 1
  done
  port="$(docker port "$NAME" 5432/tcp | head -n1 | sed 's/.*://')"
  echo "export TIPHYS_TEST_DATABASE_URL='postgres://postgres:${PASSWORD}@127.0.0.1:${port}/postgres?sslmode=disable'"
  ;;
stop)
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  ;;
*)
  echo "usage: $0 start|stop" >&2
  exit 2
  ;;
esac
