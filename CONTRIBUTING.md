# Contributing

Thanks for your interest in Portcullis. This document describes the development
workflow and the checks every change must pass.

## Prerequisites

- [Go](https://go.dev/dl/) 1.26 or newer.
- [golangci-lint](https://golangci-lint.run) and
  [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) for the
  lint and vulnerability gates:

  ```sh
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
  go install golang.org/x/vuln/cmd/govulncheck@latest
  ```

## Workflow

1. Create a branch from `main`.
2. Make your change, with tests.
3. Run `make verify` and make sure it passes.
4. Open a pull request. CI must be green before a change can merge; `main` is
   protected and does not accept direct pushes or force-pushes.

```sh
make verify   # build, vet, gofmt check, race tests, coverage, lint, govulncheck
```

## Standards

- **Tests.** New code comes with tests. The suite must hold at least 80%
  statement coverage across `internal/...`; CI enforces this.
- **Formatting.** Code must be `gofmt -s` clean. Run `make fmt` to format.
- **Linting.** `golangci-lint` must pass with the repository configuration in
  [`.golangci.yml`](.golangci.yml).
- **Architecture.** Shared types and interfaces belong in `internal/domain`;
  concrete packages depend on `domain`, never on each other, and only
  `internal/app` wires concrete implementations together. Keep the import graph
  acyclic.
- **Commits.** Write clear, imperative commit messages that explain the change
  and its motivation.

## Architecture decisions

Significant or hard-to-reverse decisions are recorded as Architecture Decision
Records under [`docs/adr`](docs/adr). If your change makes such a decision, add
an ADR for it.

## Reporting security issues

Please follow [SECURITY.md](SECURITY.md) for vulnerabilities rather than opening
a public issue.
