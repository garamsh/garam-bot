# ADR 0001: Go stack and single-module layout

> Status: accepted
> Date: 2026-07-25 (revised in place 2026-08-13, again 2026-08-14, again 2026-08-26, again 2026-10-10, again 2026-11-28)

## Context

The system's components — the workspace, the runtime, the adapters — each invite a top-level folder of their own, while `docs/convention/stack-go.md` §Directory layout governs a module that ships a binary by its Service layout, shaped `cmd/<binary>` + `internal/<domain>`, under a banned-name list. The two shapes must be reconciled without adding layers.

## Decision

One Go module at the repository root for the shipped system; the public interfaces are a second module under `api/`, which imports nothing from the shipped system's module (ADR 0032). Components map to packages and binaries, not extra top-level folders:

- Platform binaries live under `cmd/`: `cmd/garam-bot/` is the one binary, whose subcommands are `run`, `probe` and `workspace` with its operations and its MCP server (ADR 0040).
- Domain machinery lives in `internal/<domain>/` (`internal/memory/sqlite/`, `internal/workspace/`, `internal/workspace/shell/`, `internal/workspace/files/`), and what another module links goes under `pkg/`.
- End-to-end tests that wire multiple domains live in `tests/` at the module root, per `stack-go.md` §Testing.

## Consequences

- The binary holds no tool behavior: a capability reaches a bot as a tool server outside it (ADR 0035), not as a Go import.
- Conventions take precedence over component-shaped folder names (stack-go.md).
