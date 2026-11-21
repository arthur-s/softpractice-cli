# AGENTS.md

## Overview And Public Boundary

`softpractice` is the standalone open-source Go CLI and local MCP bridge for
learners working on SoftPractice practicum projects.

This is a public repository. It talks only to documented public HTTP endpoints
and local Git/filesystem state:

- Keep zero dependencies on private backend services, internal grading/QA tools,
  or local developer paths.
- Keep it usable standalone on Linux, macOS, and Windows without adjacent
  checkouts.
- Never include server secrets, internal prompts, hidden-test details, or
  machine-local paths in code, fixtures, or docs.

## Repository Layout

- `cmd/softpractice/` — CLI commands, `--json` machine envelopes (`kind`,
  `softpractice.error` with codes `usage`/`declined`, exit codes `0`–`4`), Git
  clean-tree/commit checks, `update` planner/applier, check snapshot runner, and
  the local MCP HTTP server (`127.0.0.1:39473` with a random per-run key) +
  stdio `mcp connect` + `mcp setup` (`claude-desktop`, `codex-desktop`).
- `internal/learnercli/` — public HTTP client, local credentials/config store,
  and starter `AGENTS.md`/`CLAUDE.md` guidance verification/restore.
- `internal/localchecks/` — public check execution against the working tree
  (`check`) or an isolated `HEAD` snapshot (`submit --checks`).
- `internal/starterbundle/` & `internal/submission/` — tar/gzip extraction and
  submission archive assembly with SHA-256, size, manifest, path/symlink
  safety checks, and root `AGENTS.md`/`CLAUDE.md` submission exclusion
  (subdirectory files with those names remain ordinary solution files).

## Core Invariants And Safety

- **Never overwrite uncommitted learner work:** enforce clean Git state and
  `base_content_sha256` checks before applying updates or restores; verify clean
  tree again right before modifying files.
- **Strict archive & path validation:** validate archive ref, size, SHA-256,
  file types, and relative paths (no symlinks or path traversal); `update` may
  only add or replace explicitly listed regular files.
- **Cross-platform parity:** preserve Unix/Windows specifics (`*_unix.go`,
  `*_windows.go`, `filepath` vs slash-normalized archive paths, atomic config
  file replacement).
- **Contract & doc parity:** keep `--json` output, exit codes (`0`–`4`), and MCP
  setup/connection behavior aligned across `README.md`, `README_RU.md`,
  `docs/mcp.md`, `docs/mcp_ru.md`, and `docs/agent-guidance.md`.
- **Sensitive data:** never log bearer/refresh tokens, device codes, or MCP
  per-run authentication keys.

## Context Hygiene And Verification

- Locate targets in larger files (`cmd/softpractice/main.go`, `main_test.go`,
  `learning_commands_test.go`, `mcp.go`) using `rg` and read narrow line ranges
  rather than loading entire files.
- Run focused tests during iteration (for example:
  `go test ./cmd/softpractice -run TestName` or `go test ./internal/submission/...`).
- Before handoff, match `.github/workflows/ci.yml`:
  ```bash
  gofmt -l .
  go vet ./...
  go test ./...
  go build -o /dev/null ./cmd/softpractice
  ```
- Keep English and Russian user docs (`README.md` / `README_RU.md`,
  `docs/mcp.md` / `docs/mcp_ru.md`) synchronized when user-visible flags,
  output schemas, or setup behavior change.
