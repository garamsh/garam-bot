# ADR 0040: `cmd/sherlock` goes: its workspace server and MCP server move under `garam-bot`, and `sherlock agent` and the in-house loop are removed

> Status: accepted
> Date: 2026-11-24

## Context

- **ADR 0031 supersedes the in-house agent loop.** It keeps the workspace gRPC data plane, the workspace CLI and the workspace MCP server as candidates for interfaces 2 and 3, and the memory store as a candidate for the bot's memory.
- **The repository layout drops the sherlock name** and leaves `cmd/sherlock` to this design (`garamsh/discussions` discussion 6, comment 19857166). garam-bot#1205 renames the root module `github.com/garamsh/sherlock` to `github.com/garamsh/garam-bot` before garam-bot-operator moves in.
- **Interface 3's providers reach a workspace's files and exec from outside** (ADR 0034). Interface 2's adapter runs inside it (ADR 0035). A harness without file or shell tools of its own still needs them in the workspace.
- **`cmd/sherlock` today** is `sherlock agent`, the in-house loop with its gateway, and `sherlock workspace` with its operations and its MCP server (ADR 0017, ADR 0027, ADR 0028).

## Decision

1. **`sherlock agent` is removed,** with the packages only it uses:
   - `internal/gateway/`, `internal/agent/` with its model clients `anthropic/` and `openai/`, `internal/tool/`, `internal/message/` and `tools/`;
   - the sherlock-agent image.

   The records that describe only them are deleted in the same pull request, renumbering the log as `docs/architecture/README.md` §Rules requires.
2. **The workspace server, its operations and its MCP server move under the `garam-bot` binary** as `garam-bot workspace`, `garam-bot workspace read|write|list|exec` and `garam-bot workspace mcp`. They are the in-workspace tools a harness may be given over MCP, and the workspace image's entrypoint. ADRs 0027 and 0028 are rewritten for the new names.
3. **The memory store stays** as the candidate for the bot's memory, until that interface is decided: `internal/memory/sqlite/` and `internal/agent/memory/`, moved out from under `internal/agent/`. No binary reaches them meanwhile.
4. **The `SHERLOCK_*` environment becomes `GARAM_BOT_*`,** and the images are named for garam-bot.

## Consequences

- One binary, `garam-bot`, carries `run`, `probe`, `daemon` and `workspace`.
- The gateway's hand-made mutual TLS and its issuer pin go with `sherlock agent` (garam-bot#1207).
- Every responsibility document that describes the in-house loop (`agent.md`, `gateway.md`, `security.md`, `deployment.md`) is rewritten or deleted with the removal.
- Removing and moving them is one implementation issue, after garam-bot#1205's module rename.

## Signs the decision was wrong

- An adapter needs the in-house loop's pieces (its tool budget, its memory synthesis) and rebuilds them.
- No harness uses `garam-bot workspace mcp`, because each has its own file and shell tools, so the move kept code nobody runs.
