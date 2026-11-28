# Environment

Governs the environment model: what an environment is, its topology, and its trust boundaries. Rationale: ADR 0008, ADR 0010, ADR 0027, ADR 0028, ADR 0040.

The target direction replaces the execution boundary, the workspace CLI, and the workspace MCP server described here (ADR 0031). This document describes the shipped shape until P2 of ADR 0031 lands.

## Current decisions

- The workspace is the computer a caller operates: `garam-bot workspace` serves the gRPC workspace contract — Read, Write, List, Exec — over a loopback listener. Its executor (`internal/workspace/shell`) runs caller-influenced processes in the workspace directory (ADR 0008). It holds no tool servers: a capability reaches a bot as an MCP server outside this repository (ADR 0035, ADR 0040).
- The workspace is configured from outside, like a real working volume: the workspace directory is created on first use, the listener bind address is operator-supplied, and a caller learns the listener's address from its own configuration (`--workspace-addr`, default `127.0.0.1:8081`).
- The workspace directory is the workspace's own real directory: the workspace serves the typed file API over it and execs children in it, so exec'd programs see workspace files natively — no shared mount and no path-equality trick.
- Permission enforcement lives at the data plane — the action site for files and processes. Workspace writes run under the workspace's own UID and workspace ownership is kernel-enforced (DAC), not filtered (ADR 0008, ADR 0010). A privileged workspace chowns the workspace directory to the exec UID (`exec-uid`, default 1001), keeps the directory's write bit for its own group, and gives every file it writes over the contract to the same UID, so files written through the gRPC contract and by exec'd children share one owner; an unprivileged workspace owns the directory itself. A platform that cannot run a child as the configured exec UID reports `agent_uid unavailable` instead of silently upgrading (ADR 0008).
- Exec children run under the workspace's trust boundary (ADR 0008, ADR 0010): dropped to the exec UID (workspace key `exec-uid`), bounded per call (`exec-timeout`, default 30s) and by rlimits, with a scrubbed environment and a fresh, empty network namespace. The child applies its own rlimits before it execs the command, so they need no capability and bind from its first instruction; a cap that cannot be applied fails the call instead of running the child uncapped. The per-call timeout is the one bound that is the executor's to enforce rather than the child's to carry, and enforcing it across the drop needs CAP_KILL: without it the call fails closed rather than running unbounded (#612). The unprivileged shape applies unchanged: exec-uid equal to the workspace's own UID needs no privilege, and needs two host requirements for network isolation — `user.max_user_namespaces` above 0 and a seccomp profile admitting the user+network clone, which `RuntimeDefault` does not; any other unprivileged value fails closed. It is supported beside the privileged drop, and what it does not separate is ADR 0008's (`deployment.md` §Volumes and ownership, §Capabilities).
- Untrusted exec has a 2 GiB **per-process virtual address-space** ceiling (`DefaultRlimits` in `internal/workspace/shell/process_linux.go`). This includes runtime reservations, not just resident memory: the former 512 MiB ceiling prevented the local Go runtime from starting (#833). The larger ceiling admits local Go compilation and tests while retaining a hard address-space bound; it also permits more resident allocation per process. It is not an aggregate memory budget for a process tree. Operators must bound aggregate memory with the workspace container's resource policy; CPU, file-size, open-file, dedicated-UID process-count and per-call time bounds remain in force.
- Local Go testing requires an installed toolchain and locally available dependencies: the production workspace image does not include Go (`build/workspace.Dockerfile`). Downloads remain unavailable inside the empty network namespace.
- Egress control lives at the pod network layer — the deployment-level boundary (the infrastructure layer's mechanism, not an artifact this repo ships). Process-level network isolation for exec children is the per-request netns above.
- Self-destruction is bounded by construction: files live on the workspace, and a caller's own container is unreachable from it. A broken workspace is recreated.
- A shared workspace serving several callers — one volume with several user accounts — is a later step, not the starting shape.
- Anything that can run a command operates the workspace through its contract with the `garam-bot workspace` operation subcommands — `read`, `write`, `list`, `exec` — and reaches nothing on its own host (ADR 0027). Each makes one call on the listener at `--workspace-addr`, then `GARAM_BOT_WORKSPACE_ADDR`, then `127.0.0.1:8081`, and reads no config file. They change neither the contract nor who can reach the listener. §Workspace CLI contract states what they print and how they exit.
- Plain `garam-bot workspace` serves the workspace and takes no positional argument, so a mistyped operation refuses instead of starting a server (ADR 0027).
- An MCP-capable harness operates the workspace through `garam-bot workspace mcp`, a stdio MCP server whose tools `read`, `write`, `list` and `exec` each run the matching operation subcommand once as a child process of the same binary (ADR 0028). It opens no listener and links no workspace stubs, so its only effect on its own host is running the CLI. `mcp` is not an operation: §Workspace CLI contract governs the four operations, and §Workspace MCP server governs it.

## Current state

`garam-bot workspace` serves one workspace (config key, created on first use) over the gRPC contract — Read, Write, List, Exec. It owns the executor: Exec children run in the workspace directory, dropped to the exec UID, and when privileged the workspace chowns the directory to the same UID so both writers share one owner. The workspace image hosts the gRPC server with a shell (Exec children are `sh -c` sub-processes) and an exec account at uid 1001 (`build/workspace.Dockerfile`); the deployment sets the exec UID children drop to and grants exactly the capabilities the executor needs — SETUID, SETGID, CHOWN, SYS_ADMIN, KILL (`deploy/local/`).

One binary serves both roles, so it imports `internal/workspace/server` for the `garam-bot workspace` subcommand — it is the server. The `garam-bot workspace` operation subcommands (`cmd/garam-bot/workspace_client.go`) are the binary's only client code for the listener: each dials it once per invocation, through the wire in `internal/workspace/` (ADR 0027). `garam-bot workspace mcp` (`cmd/garam-bot/workspace_mcp.go`) dials nothing: each tool call execs this binary as one of those subcommands (ADR 0028).

## Workspace CLI contract

What the `garam-bot workspace` operation subcommands send, print and exit with (ADR 0027).

| Command | Sends | Prints on success (stdout) |
|---|---|---|
| `read <path>` | Read | the file's bytes, unchanged |
| `write <path>` | Write, with the content read whole from stdin | `{"bytes_written":<n>}` |
| `list <path>` | List | `{"entries":[{"name":"…","dir":<bool>}]}` |
| `exec [--timeout <duration>] [--stdin] -- <command> [<arg>…]` | Exec | `{"stdout":"…","stderr":"…","exit_code":<n>,"timed_out":<bool>}` |

- `exec` with one positional runs it through `sh -c`; with more, the first is the executable and the rest its arguments, run without a shell. `--timeout` sets the call's timeout, and without it `exec-timeout` decides; `--stdin` forwards standard input, and without it none is read.
- `exec` always asks for an empty network namespace and never for the trusted class, an environment or a working directory: the child runs exactly as an MCP `exec` child does. The command's exit code and a workspace timeout are results, so the CLI exits 0 for them.
- Every failure prints `{"error":"<class>","code":"<gRPC code>","message":"<text>"}` on stderr, and nothing on stdout beyond the part of a result whose write failed:

| Exit | `error` | Meaning |
|---|---|---|
| 0 | — | The workspace performed the operation |
| 1 | `usage` | The CLI's own failure: it sent nothing (a bad argument or flag, a refused `--config` or `--strict-config`, an unreadable stdin, an unusable address), or it could not write the result after the workspace performed the operation, whose effect then happened and whose result stdout may hold in part |
| 2 | `workspace` | The workspace refused the call: any status it answered with, today `InvalidArgument` for a filesystem refusal such as a path outside the workspace, or `Internal` for an executor failure |
| 3 | `unreachable` | No answer: `Unavailable` (the connection failed) or `DeadlineExceeded` (the CLI's bound passed first). After a Write or an Exec was sent, its effect is unknown |

- Every call is bounded, so a workspace that stopped answering ends in exit 3: Read, Write and List at a fixed bound, Exec at `--timeout` plus a dispatch margin or, without one, at one hour, as the MCP server's `exec` does; an `exec-timeout` above an hour ends such a call in exit 3.
- Every other `garam-bot` command keeps exit 1 for any error.

## Workspace MCP server

What `garam-bot workspace mcp` offers and answers (ADR 0028).

- Transport: MCP over stdin and stdout, started by the client. No listener; stdout carries only the protocol, and logs go to stderr. Its one setting is the address it passes to each child as `--workspace-addr`: the flag, then `GARAM_BOT_WORKSPACE_ADDR`, then `127.0.0.1:8081`, with no config file. It takes no positional argument.

| Tool | Input | Runs | Result on exit 0 |
|---|---|---|---|
| `read` | `path` | `workspace read <path>` | text content when the file is UTF-8, otherwise an embedded resource holding the bytes |
| `write` | `path`, `content` | `workspace write <path>` with `content` on stdin | `{"bytes_written":n}`, as text and structured content |
| `list` | `path` | `workspace list <path>` | `{"entries":[…]}`, as text and structured content |
| `exec` | `command`; optional `args`, `stdin`, `timeout` | `workspace exec [--timeout] [--stdin] -- <command> [<args>…]` | `{"stdout","stderr","exit_code","timed_out"}`, as text and structured content |

| CLI exit | MCP answer |
|---|---|
| 0 | tool result |
| 2 | tool result with `isError: true`, the CLI's stderr object as text |
| 3 | JSON-RPC error `-32001`, `workspace unreachable: …`, the CLI's stderr object as `data` |
| 1, any other exit, or a child that could not start | JSON-RPC error `-32603`, the CLI's stderr object as `data` when it printed one |

## Open questions

- The caller↔workspace authentication (pod-local workspace listener today) — with the contract protocol work.
- Whether that contract reports the ceilings the workspace applies. An `exec` call carrying no timeout runs under `exec-timeout`, which no caller can read, so the CLI bounds such a call against a workspace that stopped answering and nothing else — an hour. Filling the timeout caller-side and requiring it each hand the command's ceiling to the caller, which is the regression #732 fixed, so the only shape leaving `exec-timeout` to decide is a method reporting what the workspace will apply — a fifth beside Read, Write, List and Exec, and a decision about this contract rather than an implementation of it.
- The shared-workspace account model — with the multi-tenancy work.
