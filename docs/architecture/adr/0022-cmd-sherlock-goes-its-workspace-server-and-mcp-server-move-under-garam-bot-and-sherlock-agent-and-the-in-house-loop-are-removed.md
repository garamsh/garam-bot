# ADR 0022: `cmd/sherlock` goes: its workspace server and MCP server move under `garam-bot`, and `sherlock agent` and the in-house loop are removed

> Status: accepted
> Date: 2026-11-24 (revised in place 2026-11-28)

## Context

- **ADR 0013 supersedes the in-house agent loop,** and keeps the memory store as a candidate for the bot's memory. Its other candidates, the workspace gRPC data plane, the workspace CLI and the workspace MCP server, were for interfaces 2 and 3, which ADR 0016 and ADR 0017 decide.
- **The repository layout drops the sherlock name** and leaves `cmd/sherlock` to this design (`garamsh/discussions` discussion 6, comment 19857166). garam-bot#1205 renames the root module `github.com/garamsh/sherlock` to `github.com/garamsh/garam-bot` before garam-bot-operator moves in.
- **Interface 3's providers reach a workspace's files and exec from outside** (ADR 0016). Interface 2's adapter runs inside it (ADR 0017). A harness without file or shell tools of its own still needs them in the workspace.
- **`cmd/sherlock` today** is `sherlock agent`, the in-house loop with its gateway, and `sherlock workspace` with its operations and its MCP server (ADR 0011, ADR 0012).

## Decision

1. **`sherlock agent` is removed,** with the packages only it uses:
   - `internal/gateway/`, `internal/agent/` with its model clients `anthropic/` and `openai/`, `internal/tool/`, `internal/message/` and `tools/`;
   - the sherlock-agent image and its `build/agent.Dockerfile`;
   - the research harnesses written in Go and their tests, which drove the agent's packages: each study's README and data stay, and the dev commit the harness code last compiled at is named in `research/README.md`;
   - the end-to-end scenarios under `tests/` that only drove the loop and the gateway; the workspace's scenarios stay.

   The records that describe only them are deleted in the same pull request, renumbering the log as `docs/architecture/README.md` §Rules requires.
2. **The workspace server, its operations and its MCP server move under the `garam-bot` binary** as `garam-bot workspace`, `garam-bot workspace read|write|list|exec` and `garam-bot workspace mcp`. They are the in-workspace tools a harness may be given over MCP, and the workspace image's entrypoint. ADR 0011 and ADR 0012 are rewritten for the new names. The workspace image, `build/workspace.Dockerfile`, stays under its name `garam-bot-workspace`: it carries the binary, its entrypoint is `garam-bot` with the command `workspace`, and `make toolchain-image` takes its toolchain from it.
3. **The memory store stays** as the candidate for the bot's memory, until that interface is decided: `internal/memory/sqlite/`, and the types it takes and returns, which moved out of `internal/agent/` into `internal/memory/` so the store imports no agent code. `internal/agent/memory/`, the agent's rolling-window adapter over the store, imports nothing that stays and goes with the agent. No binary reaches the store meanwhile.
4. **The `SHERLOCK_*` environment becomes `GARAM_BOT_*`,** and the images are named for garam-bot.

## Consequences

- One binary, `garam-bot`, carries `run`, `probe` and `workspace`; `daemon` joins it with ADR 0021.
- The gateway's hand-made mutual TLS and its issuer pin go with `sherlock agent` (garam-bot#1207).
- Every responsibility document that describes the in-house loop is rewritten or deleted with the removal: `agent.md` and `gateway.md` are deleted, and `security.md`, `deployment.md`, `environment.md`, `memory.md`, `packages.md` and `structure.md` are rewritten.
- Removing and moving them is one implementation issue, after garam-bot#1205's module rename (`bot-system.md` §Goals ④ and ⑤, G8). ADR 0011 and ADR 0012 are rewritten in that issue, with the code they describe.
- The records that described only the removed code are deleted, and the log is renumbered: seventeen records went with the agent and the gateway, and the record that put the workspace in a pod beside the agent, with the tools as scripts, went too, its workspace half now carried by ADR 0005, ADR 0006 and ADR 0011.
- The `operator/` module still names the sherlock agent in its own records and manifests. It is outside this decision and is the operator's to rewrite.

## Signs the decision was wrong

- An adapter needs the in-house loop's pieces (its tool budget, its memory synthesis) and rebuilds them.
- No harness uses `garam-bot workspace mcp`, because each has its own file and shell tools, so the move kept code nobody runs.
