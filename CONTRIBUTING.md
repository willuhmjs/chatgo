# Contributing to ChatGo

Thanks for helping! Small project, simple rules.

## Ground rules

1. **Read [docs/CONTRACTS.md](docs/CONTRACTS.md) first.** API paths, the SSE
   protocol, DB schema, and package boundaries are frozen; changes there
   need a contract update PR that lands *before* code.
2. Do **not** edit `go.mod`, `go.sum`, `web/package.json`, or
   `web/package-lock.json` in a feature PR — dependency changes are their
   own PRs.
3. Security-sensitive findings go through [SECURITY.md](SECURITY.md), not
   public issues/PRs.

## Development loop

```sh
make run        # backend on :8080 with DATA_DIR=./data
make dev-web    # vite dev server (proxies /api, /files, /v1 → :8080)
```

Before opening a PR:

```sh
make lint       # gofmt + go vet + svelte-check
make test       # go test ./... + svelte-check --threshold error
```

All SQL must be parameterized. Never log secrets, tokens, API keys, or
message content. Frontend is Svelte 5 runes only.

## Pull requests

- One concern per PR; describe *what* and *why*, link issues.
- CI runs `make lint` and `make test` — keep it green.
- New HTTP routes need a contract entry + a Go test; new components need
  svelte-check to stay clean.
- Commit messages: imperative mood, ≤72-char subject.

## Style

- Go: whatever `gofmt` says; table-driven tests; no global state in
  `internal/*` besides what `cmd/chatgo` wires.
- Web: dark-first design tokens live in `src/lib/theme.svelte.js`; match
  existing component patterns (see docs/CONTRACTS.md §Frontend).

By contributing you agree your contributions are licensed under
Apache-2.0, the license of this project.
