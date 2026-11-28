# garam-bot

garam-bot is the open-source execution core that runs bots. The execution unit is the bot; an agent (Claude Code, Codex, or a later in-house implementation) is the harness a bot uses. Control (garam) is optional — garam-bot works from files or Kubernetes CRDs without it.

## Contents
- Architecture
- Deployments
- Current state
- Running
- Development

## Architecture

Six interfaces govern every component boundary (ADR 0031, ADR 0042):

| Interface | Responsibility |
|---|---|
| Declaration spec | Bot, Workspace, WorkspaceClass, BotPool objects |
| Agent | How a harness (Claude Code, Codex) connects and operates a workspace |
| Workspace provider | Supplies isolated workspaces (the workspace gRPC contract and MCP server are candidates) |
| Control connection protocol | The optional garam plane's connection to garam-bot |
| Model gateway | Routes model requests; supports BYOK |
| Bot token | How a bot's process obtains its garam token from its place's connector, bound to a key the process holds |

The agent runs inside the workspace; the bot process runs outside. The workspace holds only short-lived gateway tokens, never the agent's real credentials. One pod is one PC: one organization's bots, a process per bot. Organizations are not mixed.

Each interface is versioned, has conformance tests, and accepts more than one implementation from the start (ADR 0031).

## Deployments

The four deployment shapes share one configuration:

| Deployment | Description |
|---|---|
| Local single | One machine, files as the declaration path |
| Cloud, your garam-bot | Your cloud host, your garam-bot instance |
| Your Kubernetes | Kubernetes CRDs as the declaration path |
| garam cloud | Managed by garam |

## Current state

The current codebase is the workspace infrastructure of the previous sherlock architecture, now under the `garam-bot` binary (ADR 0040), and the `garam-bot run` runtime of one Bot (ADR 0033). It does not yet implement the full garam-bot design above (ADR 0031).

What ships today and carries forward as interface candidates (ADR 0031):

- **Workspace gRPC contract** (Read, Write, List, Exec): the workspace provider interface.
- **Workspace MCP server** (`garam-bot workspace mcp`): the agent interface shape — Claude Code configured here operates a workspace without an in-house loop (ADR 0028).
- **Workspace CLI operations** (`garam-bot workspace read|write|list|exec`): the backing of the MCP server, and the shape a bot process uses to reach a workspace (ADR 0027).
- **The conversation memory store** (`internal/memory/sqlite`): a SQLite store of conversations, summaries and links. No binary reaches it; it stays as the candidate for the bot's memory (ADR 0040).

## Running

One CLI binary, `garam-bot`, runs a bot (`run`, `probe`) and serves a workspace (`workspace`). Build or run it from source:

```sh
go run ./cmd/garam-bot <subcommand>   # or: go build -o garam-bot ./cmd/garam-bot
```

The example workspace config under `configs/` is picked up automatically when running from the repository root and must be owner-only first:

```sh
chmod 0600 configs/workspace.yaml     # config files must be owner-only
garam-bot workspace                   # serves the workspace on 127.0.0.1:8081
```

To try the workspace in Docker instead: `make build-images` builds the images, among them the debian-slim workspace (`build/workspace.Dockerfile`), and `deploy/local/` holds the local compose setup.

### The `workspace` subcommand

Serves a workspace (ADR 0010): a gRPC data plane over loopback serving the filesystem (Read, Write, List) and process execution (Exec) in the workspace directory. The workspace owns the workspace directory and the executor; it holds no tools and no plugin loader.

```sh
garam-bot workspace
```

- The workspace owns the workspace directory (`workspace` key, created on first use); Exec children work in it by default, so they see workspace files natively. Exec children drop to the exec UID (`exec-uid` key, default 1001; single-account mode: the workspace's own UID — any other unprivileged value fails closed with `agent_uid unavailable` — which the executor runs and which is not a supported deployment shape, ADR 0008). When privileged the workspace chowns the directory to the exec UID, so files written through the gRPC contract and by exec'd children share one owner.
- The listener carries no authentication in this phase, so it serves loopback binds only — a non-loopback `--addr` fails startup (ADR 0008).
- Its config file resolves in order: `--config <path>`, then `./configs/workspace.yaml`, then the XDG config path (`~/.config/garam-bot/workspace.yaml` on Linux). Keys: `addr` (default `127.0.0.1:8081`), `workspace` (default `data/workspace`), `exec-uid` (default `1001`), `exec-timeout` (default `30s`).
- A positional argument that names no operation below is refused rather than ignored, so `garam-bot workspace raed` fails instead of starting a server.

### Operating a workspace

The `garam-bot workspace` subcommands `read`, `write`, `list` and `exec` each make one call on a running workspace's gRPC contract and nothing else on the caller's host, so a person, a script or an MCP server operates the workspace with the isolation an untrusted exec gets (ADR 0027).

```sh
garam-bot workspace list .                          # {"entries":[{"name":"notes.txt","dir":false}]}
echo hello | garam-bot workspace write notes.txt    # {"bytes_written":6}
garam-bot workspace read notes.txt                  # the file's bytes, unchanged
garam-bot workspace exec -- 'ls -la; exit 3'        # {"stdout":"…","stderr":"","exit_code":3,"timed_out":false}
garam-bot workspace exec --stdin -- wc -c < notes.txt
```

- The address is `--workspace-addr`, then `GARAM_BOT_WORKSPACE_ADDR`, then `127.0.0.1:8081`. No config file is read, and `--config` or `--strict-config` is refused.
- `exec` runs one positional through `sh -c`, and more than one as an executable with its arguments. It always runs the child without network, under the resource caps, with the scrubbed environment, in the workspace directory. `--timeout` sets the command's timeout, and without it the workspace's `exec-timeout` applies. `--stdin` forwards standard input.
- The command's own exit code is part of the result. The CLI's exit status is about the call: `0` for performed, `1` for the CLI's own failure (refused before any call, or unable to write the result of an operation the workspace performed), `2` for refused by the workspace (a path outside it, an executable it cannot start), and `3` for an unreachable workspace or one that did not answer. Every failure prints one JSON object on stderr, `{"error":"usage|workspace|unreachable","code":"<gRPC code>","message":"…"}` (`docs/architecture/environment.md` §Workspace CLI contract).

### Serving a workspace to an MCP client

`garam-bot workspace mcp` is a stdio MCP server whose tools `read`, `write`, `list` and `exec` each run the matching operation above once, as a child process of the same binary (ADR 0028). An MCP-capable agent with its local tools disabled then has these four as its only tools, so the workspace is what its tool calls act on; what that leaves running on its own host is listed below. The client starts it from a command line, for example in an MCP configuration file:

```json
{"mcpServers": {"garam-bot-workspace": {"command": "garam-bot", "args": ["workspace", "mcp", "--workspace-addr", "127.0.0.1:8081"]}}}
```

- Its one setting is the address, resolved as the operations resolve it. It opens no listener and takes no positional argument.
- A workspace refusal (exit 2) comes back as a tool result marked as an error; an unreachable workspace (exit 3) as JSON-RPC error `-32001`; any other failure as `-32603` (`docs/architecture/environment.md` §Workspace MCP server).

#### Claude Code as the client

Checked against Claude Code 2.1.302, the version `claude --version` printed; each flag's quoted effect is from that version's `claude --help`. With a workspace serving on `127.0.0.1:8081` and `garam-bot` on `PATH`:

```sh
claude -p "List the workspace's top directory, then run pwd in it." \
  --tools "" \
  --strict-mcp-config \
  --mcp-config '{"mcpServers": {"garam-bot-workspace": {"command": "garam-bot", "args": ["workspace", "mcp", "--workspace-addr", "127.0.0.1:8081"]}}}' \
  --allowedTools 'mcp__garam-bot-workspace__*'
```

- `--tools ""` turns off Claude Code's built-in tools: the flag specifies "the list of available tools from the built-in set", and `""` is to "disable all tools".
- `--strict-mcp-config` makes the server above the only one: "Only use MCP servers from --mcp-config, ignoring all other MCP configurations".
- `--mcp-config` loads that server from a JSON string, the configuration shown earlier in this section.
- `--allowedTools` allows the server's tools without a permission prompt, which a `-p` run has nobody to answer. `mcp__<server>__*` matching every tool of one server is from Claude Code's permissions documentation (`https://code.claude.com/docs/en/permissions`, "To match every tool from an MCP server").

#### What this setup does not cover

- **Mechanisms the client runs on its own host that are not tools.** `--tools ""` names the built-in tool set only. Claude Code's `--help` names hooks "defined in settings and by installed plugins" (under `--bare`), and its permissions documentation describes hooks as "custom shell commands" Claude Code runs (§Extend permissions with hooks). Turn those off, or review what they run, before relying on this setup: `claude --help` states that `--bare` skips them, and that it also changes how Claude Code authenticates. For any other client, find and disable its equivalents; this section states nothing about them.
- **The workspace's isolation is the workspace's own.** An `exec` child gets no network, the resource caps, the scrubbed environment and the workspace directory (ADR 0027 §3, "Exec runs under the isolation an untrusted exec gets"), and the MCP server's only effect on the client's host is running the operation CLI (ADR 0028 §3, "The server's only effect on its own host is running the CLI"). Nothing here adds isolation beyond that.
- **A workspace run directly on the host separates less than one in its own container.** `docs/architecture/deployment.md` §Volumes and ownership rests the separation of an `exec` child from other files on the workspace container's mounts and on the `exec-uid`. A workspace started as a host process has no such mount boundary, and the executor gives an `exec` child a new network namespace (and a user namespace when unprivileged) but no mount namespace or changed root (`internal/workspace/shell/process_linux.go`), so its `exec` children read and write whatever files their `exec-uid` can on that host.

### Configuration

Settings resolve in order: **flags > environment variables > config file > defaults**. Environment variables use the `GARAM_BOT_` prefix (`GARAM_BOT_WORKSPACE`, `GARAM_BOT_WORKSPACE_ADDR`, …). The workspace's config file resolves in order: `--config <path>`, then `./configs/workspace.yaml`, then the XDG config path (`~/.config/garam-bot/workspace.yaml` on Linux). A missing file is an error only when `--config` names it explicitly. A loaded config file must be owner-only (`0600` or `0400`); a group/other-readable file fails startup (docs/architecture/security.md). A key the file carries that no setting reads — a typo, a renamed key, a block left from a removed feature — is reported at startup by a WARN naming the key and the file, and does not stop the process; `--strict-config` makes it refuse instead.

Workspace keys (`garam-bot workspace`; see `configs/workspace.yaml` for an example):

| Key | Flag | Default |
|---|---|---|
| `addr` | `--addr` | `127.0.0.1:8081` (workspace gRPC listen address; loopback-only) |
| `workspace` | `--workspace` | `data/workspace` (workspace directory, created on first use; Exec children work here by default) |
| `exec-uid` | `--exec-uid` | `1001` (UID Exec children run as; single-account mode, not a supported deployment shape (ADR 0008): set to the workspace's own UID — any other unprivileged value fails closed with `agent_uid unavailable`; a privileged workspace also chowns the workspace directory to it) |
| `exec-timeout` | `--exec-timeout` | `30s` (per-call ceiling for Exec commands) |

The example config under `configs/` is `configs/workspace.yaml`.

To work on the codebase you need Go and GNU Make — `make test` runs the Go tests. The standard checks are:

```sh
make ci      # lint + format check + test + build (same as CI)
```

Individual targets: `make lint`, `make format`, `make test`, `make build`.

`make test-privileged` requires root and the capabilities authored in
`deploy/local/docker-compose.yml`. It narrows the run to that set and exercises
both the shell tests and shipped-workspace Exec with a distinct exec UID.
The binary cases verify credentials, shared file ownership, environment and
network isolation, resource limits, and deadlines; missing privilege fails
instead of skipping. The privileged CI job invokes the same target on PRs to
`main`.

## Development

Beyond Go and GNU Make, the toolchain is:

- **golangci-lint** — `make lint` runs it from your PATH, with its cache in `.cache/golangci-lint` inside the checkout, so a result recorded by another checkout never reaches it. CI installs `v2.15.0` through `golangci-lint-action` (`.github/workflows/ci.yml`), which is the one place the version is enforced; `.golangci.yml` carries it in a comment beside the config-schema version it declares.
- **buf** — `make proto` lints the proto files, regenerates the workspace gRPC stubs (`buf.yaml` + `buf.gen.yaml`), and fails if a regenerated stub is uncommitted; the CLI version is pinned in `.github/workflows/ci.yml` (1.72.0).
- **lefthook** — `make hooks` installs the git hooks once after cloning (pre-commit lint/format, pre-push test).

Every target exports `GOTOOLCHAIN` as a minimum built from `go.mod`'s `go` directive, so a go command run through `make` builds each tool it fetches with at least that toolchain — `go install tool@version` otherwise takes the toolchain from the tool's own `go.mod` and can hand you a binary that cannot load this module's packages. A `go install` typed outside `make` is not covered by it and takes the floor from your own environment.

This repository is developed by AI agents under written conventions — see `AGENTS.md` for what a contributor is held to and `docs/convention/` for the rules all code follows. Internal structure and design decisions live in `docs/architecture/`.
