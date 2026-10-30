# Contributing

Thanks for helping. Issues and pull requests are welcome; for anything larger
than a small fix, open an issue first so the design can be agreed on.

## Setup and checks

[README.md](README.md) explains how to run the portal locally (Docker Compose,
no cluster needed) and the frontend against its mock API. Before you open a
pull request, run what CI runs:

```sh
go build ./cmd/... ./internal/... && go vet ./cmd/... ./internal/...
eval "$(deploy/sql/test-db.sh start)"          # PostgreSQL for the integration tests
go test -race ./cmd/... ./internal/...
deploy/sql/test-db.sh stop
cd web && npm ci && npm run typecheck && npm run lint && npm test && npm run build
```

## Ground rules

- The architecture rules in [CLAUDE.md](CLAUDE.md) apply: dependencies point
  inward, every authorization decision goes through `AccessService` →
  `domain.Decide` (or `DecideApp`), `kubeaccess` is the only code with
  Kubernetes clients and takes typed verbs only, and an action does not run
  if its "started" audit event cannot be written.
- Keep changes small and in the existing style, with tests next to the code
  they cover.
- Update the docs with the behavior: [spec.md](spec.md) for what the portal
  does, [docs/api.md](docs/api.md) for the HTTP contract, and the other files
  in [docs/](docs/) where they apply.
- Changes to `Decide`, `kubeaccess`, impersonation, the chart's RBAC or
  admission policies, sessions or audit get an extra careful review; say in
  the pull request what could go wrong and how the tests cover it.
- Security problems: see [SECURITY.md](SECURITY.md), not public issues.

By contributing you agree that your contribution is licensed under the
Apache License 2.0 (see [LICENSE](LICENSE)).
