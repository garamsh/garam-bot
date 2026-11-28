# Security

Governs the security model: trust domains, secret custody, and egress. Rationale: ADR 0003, ADR 0005, ADR 0006, ADR 0015, ADR 0019, ADR 0022.

The target direction replaces the execution boundary and the deployment topology described here (ADR 0013). This document describes the shipped shape until P2 of ADR 0013 lands.

## Current decisions

- Two kinds of container, two trust postures:
  - **The runtime container that runs one Bot (`garam-bot run`)** holds one secret, the bot's garam token. It reads the token from a file the Pod mounts read-only, the token's value reaches no log line, error or status file, and a read that fails names the file and never the value (ADR 0015, `runtime.md`). It starts no other process and runs nothing a model supplies.
  - **The workspace (`garam-bot workspace`)** serves the gRPC data plane (files and exec) and owns the one untrusted-code path: Exec children run as the unprivileged exec UID with a scrubbed environment, rlimits, and a fresh network namespace (ADR 0005, ADR 0006). It enforces its caller's permissions at the action site with Linux users and modes.
- Secrets never materialize in the execution domain — the workspace and the Exec children it runs — and exposure there is prevented by absence, not filtering (ADR 0003).
- **The workspace's Exec path is the only place untrusted code runs.** A command a caller supplies runs as an Exec child in the workspace and can reach only the workspace, which is recreated. Where the memory store's volume is shared into the workspace container, the distinct exec UID of the privileged shape stops reads and writes of its files, which are owner-only, and leaves their names visible (ADR 0005, `deployment.md` §Volumes and ownership).
- **Where the execution boundary sits depends on the shape, and the operator chooses it** (ADR 0005). In the privileged shape a uid separates an Exec child from the workspace process. In the unprivileged shape, which PodSecurity `restricted` admits, the child runs as the workspace's own uid: the mount keeps it from other volumes, absence and a non-dumpable workspace keep secrets from it, and a per-call network namespace isolates it, but it reads what the workspace's uid can — the workspace's config file included — and **it can signal the workspace server, stopping the data plane until the container restarts.** The shape needs two host requirements, both infra artifacts: `user.max_user_namespaces` above `0`, and a seccomp profile admitting `unshare` and `clone` with `CLONE_NEWUSER|CLONE_NEWNET` — under `restricted`, a `Localhost` profile on every node, named by the workspace container alone while the caller's container stays on `RuntimeDefault`, since `RuntimeDefault` refuses the clone. The sysctl alone leaves every `NoNetwork` Exec refused (`deployment.md` §Capabilities). In both, `NoNetwork` means a fresh, empty network namespace and nothing weaker; where one cannot be made the call is refused (ADR 0006).
- Image hygiene stays a security property: minimal images, no unneeded setuid binaries, dropped capabilities. The workspace's image carries the shell its Exec path runs.
- A shared workspace means multi-tenant execution — per-caller accounts on one workspace — and is deferred until the dedicated-workspace baseline exists.

## Current state

The workspace (`garam-bot workspace`) is the data plane and owns the untrusted-code path: Exec children run through its executor (`internal/workspace/shell`) dropped to the configured exec UID in the workspace directory, with a scrubbed environment, trust-class rlimits, a per-call timeout, and a fresh, empty network namespace (privileged unshare, or unprivileged userns+netns in the unprivileged shape, ADR 0005, ADR 0006). A drop the platform cannot perform fails closed with `agent_uid unavailable`, a resource cap it cannot apply fails closed the same way, and so does a deadline it could not enforce — signalling the account it drops to needs `CAP_KILL` (#612) — so no untrusted child runs uncapped or unbounded (ADR 0005, ADR 0006). In the unprivileged shape the workspace marks itself non-dumpable, so a same-account child cannot read its `/proc/<pid>/environ` (ADR 0003). When privileged the workspace chowns the workspace directory to the exec UID, so writes over the contract and exec'd children share one owner.

The workspace's gRPC listener is unauthenticated in this phase, so it serves loopback binds only: an unauthenticated gRPC listener carrying Exec is a remote shell into the workspace, and a non-loopback addr fails startup. The operations (`garam-bot workspace read|write|list|exec`) and the MCP server (`garam-bot workspace mcp`) reach it only through that listener (ADR 0011, ADR 0012).

The in-house agent, its gateway and its tool client are removed (ADR 0022); the model, tool and channel credentials they held have no holder in this repository. Where a bot's credentials live is the declaration spec's and the connector's to state (ADR 0019, ADR 0024).

## Rejected alternatives

- **Filters or guard models as the boundary** — probabilistic and bypassable by obfuscation; boundaries are structural only.
- **In-body secret substitution (shell wrapper, FUSE, LD_PRELOAD)** — a materialized secret is a stolen secret (ADR 0003).
- **Shared workspace from the start** — multi-tenant execution security before the dedicated baseline exists.

## Open questions

- Authentication between a workspace's caller and the workspace (how the workspace knows the caller is its pod's) — the gRPC listener ships pod-local and unauthenticated until this is decided. In the unprivileged shape, a key the workspace holds for it is readable by every Exec child, which runs as the workspace's uid (ADR 0005).
- The shared-workspace account model — with the multi-tenancy work.
