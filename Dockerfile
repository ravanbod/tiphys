# Base images come from Docker Hub; BASE_REGISTRY points at a mirror instead
# (it replaces docker.io/library, e.g. --build-arg BASE_REGISTRY=mirror.example.com).
ARG BASE_REGISTRY=docker.io/library

# Frontend build
FROM ${BASE_REGISTRY}/node:22.16.0-alpine AS web
ARG NPM_REGISTRY=
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && npm ci
COPY web/ ./
RUN npm run build

# Backend build, with the frontend embedded
FROM ${BASE_REGISTRY}/golang:1.27.1 AS builder

# A private module proxy may need GOSUMDB=off as well.
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GO111MODULE=on \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

WORKDIR /app

COPY . .
COPY --from=web /web/dist/ internal/transport/serverhttp/webdist/
# `go build` fetches only the modules it compiles (`go mod download` would also
# fetch unused ones); the cache mount keeps them between builds.
RUN --mount=type=cache,target=/go/pkg/mod go build -o /output/server ./cmd/server

FROM ${BASE_REGISTRY}/alpine:3.21.3

RUN addgroup -S tiphys && adduser -S -G tiphys tiphys \
 && mkdir -p /var/lib/tiphys/clusters && chown tiphys:tiphys /var/lib/tiphys/clusters
WORKDIR /app
COPY --from=builder /output/server /app/server

USER tiphys
EXPOSE 8080 9090
ENTRYPOINT ["/app/server"]
