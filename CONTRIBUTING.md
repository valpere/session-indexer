# Contributing

session-indexer is a solo-maintained project, but issues, bug reports, and
pull requests are welcome.

## Before opening an issue

Check the [FAQ](README.md#faq) and [Troubleshooting](README.md#troubleshooting)
sections of the README first — most "why doesn't X work" questions are
answered there (Ollama unavailable, schema version mismatch, worktree DB
visibility, etc).

For bug reports, include:
- `session-indexer` version (`session-indexer --version`)
- The command you ran and its full output
- Go version (`go version`) and OS

## Development setup

```bash
git clone https://github.com/valpere/session-indexer
cd session-indexer
go build -o bin/session-indexer ./cmd/session-indexer   # or: make build
go test ./...                                            # or: make test
go vet ./...                                              # or: make vet
gofmt -l .                                                # should print nothing
```

Requires Go 1.26.6+ (see `go.mod`) and, for embedding-related work, a local
[Ollama](https://ollama.com/download) with `bge-m3:latest` pulled. Tests that
don't touch `internal/embed` run without Ollama.

Run `make help` for the full list of Makefile targets.

## Making a change

1. Fork the repo and branch from `main`.
2. Keep changes focused — one logical change per PR. Match the existing
   style in the package you're touching (see `CLAUDE.md` for the project's
   own architecture notes and conventions if you want the fuller picture).
3. Add or update tests for any behavior change. `go test -race ./...` must
   pass.
4. Update `docs/architecture.md` or `docs/requirements.md` if your change
   affects them — stale docs are worse than no docs.
5. Open a PR against `main` with a clear description of what changed and
   why. Reference any related issue.

## Code review

This repo uses an automated multi-model review pipeline
(`.claude/skills/fix-review/`) for PRs opened via Claude Code. If you're
contributing from outside that workflow, a manual review by the maintainer
covers the same ground — no special process required on your end.

## Reporting a security issue

Please **do not** open a public issue for a security vulnerability. See
[`SECURITY.md`](.github/SECURITY.md) if present, or open a private
[security advisory](https://github.com/valpere/session-indexer/security/advisories/new)
on GitHub instead.

## License

By contributing, you agree that your contributions will be licensed under
the project's [Apache License 2.0](LICENSE).
