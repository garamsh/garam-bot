# Runtime

Governs the runtime that runs one Bot: what `garam-bot run` reads, the status contract a manager reads to learn the bot's process runs, and what the runtime image asks of the Pod that runs it. What a Bot is, is interface 1's (`api/declaration/v1alpha1/README.md`).

## Current decisions

### What it reads

- **One Bot, from `--bot-file`,** in interface 1's file form, read once at start with `manifest.Parse` (ADR 0015). A flag not given, or a file that is absent, unreadable, not one admissible object or not a Bot fails the start with `BotInvalid`. The Bot's `metadata.namespace` and `metadata.generation`, where the file carries them, are what the status reports.
- **The bot's garam token, from its place's connector, where `--connector-url` is given** (ADR 0024, interface 6 `api/bot-token/v1alpha1/`).
  - **The key.** At start the process makes an ES256 key, kept in memory and never written or sent. The token is bound to it, so a new process obtains a new token.
  - **What it reads.** `--connector-trust-file` holds the certificates the connector is trusted by, and is read again at each attempt so a replaced certificate is trusted. `--credential-file` holds the place's credential naming the bot, and is read again for each request because the place replaces it. A URL given without both files fails the start with `Failed`.
  - **When.** It obtains the token at start, then again once two thirds of its life have passed, and at once when it is told garam refused the token held with 401 (`TokenSource.Refused`), which drops that token. An attempt that fails, whether the connector is unreachable or refuses, keeps the token held, marks it stale and is retried after 1 second, doubling to 60, or after the `Retry-After` the connector answers. The bot goes on running.
  - **Expiry.** A token is not answered once its `expires_in` has passed, whether or not a new one was obtained.
  - **No token.** Without the flag, the bot runs with no garam identity, as a cluster-owned bot does today.
  - **Confinement.** The token, the proof and the credential reach no log line, error or status. The token is held in memory alone.
  - **Not yet presented.** No garam route accepts a bot token yet (`bot-system.md` §Authentication), so the process holds a token nothing presents, and nothing calls `TokenSource.Refused` until the first caller.
- **What the bot does.** It holds its Bot until SIGTERM, which exits 0. It has no work source until interfaces 2 to 4 are built (ADR 0013).

### The status contract

- **The status file.** `run` writes it at `--status-file`, `/run/garam-bot/status.json` by default, every 5 seconds, replacing it by a rename. It holds:
  - `state`: `Running`, or `Stopped` once SIGTERM has stopped the bot;
  - `heartbeat`: when it was written;
  - `bot`: namespace, name and generation;
  - `token`: when the token held was obtained (`obtainedAt`) and when it expires (`expiresAt`), both absent before the first, and whether the latest attempt failed (`stale`). It is absent for a bot with no connector.
- **`garam-bot probe`** exits 0 only while the file says `Running` and its heartbeat is at most 30 seconds old. Otherwise it exits 1 and prints why. The Pod declares it as the container's readiness probe and liveness probe, so the kubelet reports the answer on the Pod, and nothing dials the container.
- **A failed start** exits 1 and writes `<Reason>: <message>` to `--termination-log`, `/dev/termination-log` by default, which the kubelet reports as the container's termination message. `Reason` is `BotInvalid` or `Failed`, the second for a start the first does not name, such as a `--connector-url` without its files.
- **How a manager reads it.** A Bot's `Running` condition (interface 1 STA-1) is `True` while the container is ready and `False` otherwise. While the container is not ready, the condition's reason is the termination message's reason where the container last terminated, and `Starting` otherwise. Writing the condition is the manager's.

### What the image asks of the Pod

- **User.** It runs as uid and gid 65532 (`build/runtime.Dockerfile`), named by number in the image so the kubelet can verify it, with no capability. So the Pod can assert `runAsNonRoot` without a `runAsUser`, drop every capability, forbid privilege escalation and use the RuntimeDefault seccomp profile, which is what PodSecurity `restricted` asks.
- **Status directory.** `/run/garam-bot` is writable by that user in the image. A Pod with a read-only root filesystem mounts an `emptyDir` there.
- **Files.** The Bot file, the connector's trust file and the credential file are mounted read-only, and readable by uid 65532 or the Pod's group. At a cluster the credential is the Bot's projected ServiceAccount token (garam-bot-operator ADR 0097).
- **Network.** It reaches its connector, at a cluster a Service in the release namespace.
- **Image.** It is `garam-bot-runtime:<revision>` from `make build-images`, published to `garam-bot/runtime` and referenced by digest (`deployment.md` §Images and tags).

## Rationale

- ADR 0015: why a separate binary, why an exec probe over an endpoint, and the image's shape.
- ADR 0024: why the process holds the key its token is bound to, and obtains the token from its connector rather than from a file.
- ADR 0014: the file form it reads.

## Open questions

- **Several bots in one process** (one pod one PC, ADR 0013), which waits for a runtime that runs them.
- **A stale or expiring token as its own condition**, rather than only in the status file.
