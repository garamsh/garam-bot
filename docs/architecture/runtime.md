# Runtime

Governs the runtime that runs one Bot: what `garam-bot run` reads, the status contract a manager reads to learn the bot's process runs, and what the runtime image asks of the Pod that runs it. What a Bot is, is interface 1's (`api/declaration/v1alpha1/README.md`).

## Current decisions

### What it reads

- **One Bot, from `--bot-file`,** in interface 1's file form, read once at start with `manifest.Parse` (ADR 0033). A flag not given, or a file that is absent, unreadable, not one admissible object or not a Bot fails the start with `BotInvalid`. The Bot's `metadata.namespace` and `metadata.generation`, where the file carries them, are what the status reports.
- **The bot's garam token, from `--token-file`, when given.**
  - **At start.** It is read once, and a file that is unreadable or empty after trimming fails the start with `TokenUnreadable`.
  - **While running.** It is read again every `--token-interval`, 10 seconds by default, so a token the kubelet swaps into a Secret volume is held within that interval. A read that fails keeps the last token and marks it stale, and the bot goes on running.
  - **No token.** Without the flag, the bot runs with no garam identity, as a cluster-owned bot does today.
  - **Confinement.** The token's value reaches no log line, error or status.
- **What the bot does.** It holds its Bot until SIGTERM, which exits 0. It has no work source until interfaces 2 to 4 are built (ADR 0031).

### The status contract

- **The status file.** `run` writes it at `--status-file`, `/run/garam-bot/status.json` by default, every 5 seconds, replacing it by a rename. It holds:
  - `state`: `Running`, or `Stopped` once SIGTERM has stopped the bot;
  - `heartbeat`: when it was written;
  - `bot`: namespace, name and generation;
  - `token`: when it was last read and whether it is `stale`. It is absent for a bot with no token.
- **`garam-bot probe`** exits 0 only while the file says `Running` and its heartbeat is at most 30 seconds old. Otherwise it exits 1 and prints why. The Pod declares it as the container's readiness probe and liveness probe, so the kubelet reports the answer on the Pod, and nothing dials the container.
- **A failed start** exits 1 and writes `<Reason>: <message>` to `--termination-log`, `/dev/termination-log` by default, which the kubelet reports as the container's termination message. `Reason` is `BotInvalid`, `TokenUnreadable` or `Failed`, the last for a start the other two do not name, such as a `--token-interval` that is not positive.
- **How a manager reads it.** A Bot's `Running` condition (interface 1 STA-1) is `True` while the container is ready and `False` otherwise. While the container is not ready, the condition's reason is the termination message's reason where the container last terminated, and `Starting` otherwise. Writing the condition is the manager's.

### What the image asks of the Pod

- **User.** It runs as uid and gid 65532 (`build/runtime.Dockerfile`), named by number in the image so the kubelet can verify it, with no capability. So the Pod can assert `runAsNonRoot` without a `runAsUser`, drop every capability, forbid privilege escalation and use the RuntimeDefault seccomp profile, which is what PodSecurity `restricted` asks.
- **Status directory.** `/run/garam-bot` is writable by that user in the image. A Pod with a read-only root filesystem mounts an `emptyDir` there.
- **Files.** The Bot file and the token file are mounted read-only, and the token file is readable by uid 65532 or the Pod's group.
- **Image.** It is `garam-bot-runtime:<revision>` from `make build-images`, published to `garam-bot/runtime` and referenced by digest (`deployment.md` §Images and tags).

## Rationale

- ADR 0033: why a separate binary, why an exec probe over an endpoint, and the image's shape.
- ADR 0032: the file form it reads.

## Open questions

- **Several bots in one process** (one pod one PC, ADR 0031), which waits for a runtime that runs them.
- **A stale or expiring token as its own condition**, rather than only in the status file.
