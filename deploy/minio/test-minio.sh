#!/usr/bin/env bash
# Throw-away MinIO for the recording integration test.
#
#   eval "$(deploy/minio/test-minio.sh start)"   # starts it, exports TIPHYS_TEST_MINIO_*
#   go test ./internal/infra/recording -run Integration -v
#   deploy/minio/test-minio.sh stop
#
# MINIO_IMAGE overrides the image. minio/minio is no longer published on
# Docker Hub, so the default is the Chainguard build (same `server /data`).
set -euo pipefail

NAME=tiphys-test-minio
PORT=${TIPHYS_TEST_MINIO_PORT:-19000}
IMAGE=${MINIO_IMAGE:-cgr.dev/chainguard/minio:latest}
USER_=tiphys-test
PASS_=tiphys-test-secret

case "${1:-}" in
start)
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  # tmpfs: nothing to clean up, and writable for the image's non-root user.
  docker run -d --name "$NAME" -p "127.0.0.1:$PORT:9000" \
    -e MINIO_ROOT_USER="$USER_" -e MINIO_ROOT_PASSWORD="$PASS_" \
    --tmpfs /data:rw,mode=1777 \
    "$IMAGE" server /data >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS "http://127.0.0.1:$PORT/minio/health/ready" >/dev/null 2>&1; then
      echo "export TIPHYS_TEST_MINIO_ENDPOINT=127.0.0.1:$PORT"
      echo "export TIPHYS_TEST_MINIO_ACCESS_KEY=$USER_"
      echo "export TIPHYS_TEST_MINIO_SECRET_KEY=$PASS_"
      exit 0
    fi
    sleep 1
  done
  docker logs "$NAME" >&2 || true
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "minio did not become ready" >&2
  exit 1
  ;;
stop)
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  ;;
*)
  echo "usage: $0 start|stop" >&2
  exit 2
  ;;
esac
