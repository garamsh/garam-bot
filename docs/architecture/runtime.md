# Runtime

Governs the runtime that runs one Bot: what `garam-bot run` reads, the status contract a manager reads to learn the bot's process runs, and what the runtime image asks of the Pod that runs it. What a Bot is, is interface 1's (`api/declaration/v1alpha1/README.md`).

## Current decisions

### What it reads

- **One Bot, from `--bot-file`,** in interface 1's file form, read once at start with `manifest.Parse` (ADR 0033). A flag not given, or a file that is absent, unreadable, not one admissible object or not a Bot fails the start with `BotInvalid`. The Bot's `metadata.namespace` and `metadata.generation`, where the file carries them, are what the status reports.
- **The bot's garam token, from its place's connector, where `--connector-url` and `--credential-file` are given** (ADR 0042, interface 6 `api/bot-token/v1alpha1/`).
  - **The key.** At start the process makes one ES256 key, kept in memory and never written or sent. The token and every interface 7 proof are bound to it, so a new process obtains a new token and pins a new key.
  - **What it reads.** `--connector-trust-file` holds the certificates the connector is trusted by, and is read again at each attempt so a replaced certificate is trusted. `--credential-file` holds the place's credential naming the bot, and is read again for each request because the place replaces it. A URL given without the trust file, or without either credential file, fails the start with `Failed`, and so does a credential file given without a URL.
  - **When.** It obtains the token at start, then again once two thirds of its life have passed, and at once when it is told garam refused the token held with 401 (`TokenSource.Refused`), which drops that token. An attempt that fails, whether the connector is unreachable or refuses, keeps the token held, marks it stale and is retried after 1 second, doubling to 60, or after the `Retry-After` the connector answers. The bot goes on running.
  - **Expiry.** A token is not answered once its `expires_in` has passed, whether or not a new one was obtained.
  - **No token.** Without `--credential-file`, the bot runs with no garam identity, as a cluster-owned bot does.
  - **Confinement.** The token, the proof and the credential reach no log line, error or status. The token is held in memory alone.
  - **Not yet presented.** No garam route accepts a bot token yet (`bot-system.md` §Authentication), so the process holds a token nothing presents, and nothing calls `TokenSource.Refused` until the first caller.
- **The workspace of the Bot's first attachment, through its connector, where `--connector-url` and `--workspace-credential-file` are given** (ADR 0043, interface 7 `api/workspace-access/v1alpha1/`). A Bot with no attachment runs with no workspace. It is `Workspace` in `internal/bot`.
  - **What it reads.** `--workspace-credential-file` holds the place's credential of interface 7's audience, read again for each request. The connector's trust is `--connector-trust-file`'s, as interface 6's is.
  - **Acquiring.** It acquires the workspace until it runs. It reads a pending workspace again with a `GET` no sooner than its `Retry-After`, or after a back-off from 1 second doubling to 60 where it has none (WS-3), and acquires a stopped or failed one again, which starts or recovers it.
  - **Token files.** After every acquire it writes a fresh adapter token, at least 128 random bits (AUTH-1), at `/run/garam-bot/agent/token`. It then obtains the session's tokens and writes them, with another fresh adapter token, in one write: the model token at `/run/garam-bot/model/token` and each tool's at `/run/garam-bot/tools/<tool>/token`. It writes them again after 40 minutes, or two thirds of the shortest token's life where that is sooner (TOK-2), and after any acquire, since one that started or recovered the workspace left no token there (WS-1).
  - **No issuer.** While the connector answers `waiting_for_gateway`, the adapter's token is written, the status says `waitingForGateway`, and the session tokens are asked for again after the back-off.
  - **Not running.** A write or a dial answered `not_running` makes it acquire again.
  - **Waits.** After a 401 it waits before its next request from 1 second, doubling to 60, until an answer is not 401 (AUTH-2). A 429 or 503 with `Retry-After` waits at least that, at most 60 seconds (REN-1). Any other failure waits from 1 second, doubling to 60, and the bot goes on running.
  - **The dial.** `Workspace.Dial` opens one connection to the adapter's port, offering HTTP/1.1 alone (DIAL-1). The driver below dials through it.
  - **Confinement.** No token, credential, proof or file content reaches a log line, error or the status. The status names a refusal by its code alone.
- **What the bot does.** It holds its Bot until SIGTERM, which exits 0. With a workspace, it drives its adapter.
- **Driving its adapter** (ADR 0043 decision 7, interface 2 `api/agent/v1alpha1/`). It is `Driver` in `internal/bot`, interface 2's caller, and it starts with the workspace.
  - **The connection.** Each request goes over a connection `Workspace.Dial` opens, at most two at once, and carries the adapter token the workspace last wrote, read again for each request (AUTH-1).
  - **The session.** Once the workspace runs with its session's tokens written, it checks the adapter's `info` against `spec.agent.harness`, and opens no session on another harness. It then opens the session `bot` with the Bot's name, `spec.persona.instructions`, the workspace's model gateway and tools, and the token file of each. A session the adapter holds under another spec, from an earlier process or generation, is deleted and opened again.
  - **Tasks.** Until G4's long poll, the tasks come from `--inbox-dir`: each regular file is one message, given in the order of the names, one task at a time. A file's name is the task's identifier, its content the input. A name starting with `.` is skipped, so a writer can rename a file into place, and so is a name that is no task identifier. A file is removed once its task is done. Without `--inbox-dir`, the session is opened and given no task.
  - **Events.** It long-polls the session's events, holding up to 50 seconds (`MaxWait`), from the last it read, and skips a type it does not know (VER-2). A task is done at its `taskDone`.
  - **Dialling again.** A connection the adapter breaks, or a dial that fails, is dialled again after a back-off from 1 second, doubling to 60, reset by a dial answered 101 (DIAL-2). It reads on from the last event it read, and gives the current task again, which the adapter runs once (TASK-1).
  - **Failures.** Any other failure, a refusal or the harness mismatch among them, leaves it running and is tried again after a back-off from 1 second, doubling to 60, reset once the session moves. The status file names the reason.
  - **Confinement.** No input, event content, file content or token reaches a log line or the status file. The log names task identifiers, event types and outcomes alone.

### The status contract

- **The status file.** `run` writes it at `--status-file`, `/run/garam-bot/status.json` by default, every 5 seconds, replacing it by a rename. It holds:
  - `state`: `Running`, or `Stopped` once SIGTERM has stopped the bot;
  - `heartbeat`: when it was written;
  - `bot`: namespace, name and generation;
  - `token`: when the token held was obtained (`obtainedAt`) and when it expires (`expiresAt`), both absent before the first, and whether the latest attempt failed (`stale`). It is absent for a bot with no garam identity.
  - `workspace`: the `attachment`, its `phase` and `reason` as the connector last answered, `waitingForGateway`, when the session's tokens were last written (`tokensWrittenAt`), and the code of the latest refusal (`refusal`). It is absent for a bot with no workspace.
  - `session`: the session's `state`, the `reason` no task runs where none can (`waitingForWorkspace`, `harnessMismatch` or `adapterUnavailable`), the last task's `id` and `outcome` (`lastTask`), and how many tasks were `completed`, `failed` and `stopped`. It is absent for a bot with no workspace.
- **`garam-bot probe`** exits 0 only while the file says `Running` and its heartbeat is at most 30 seconds old. Otherwise it exits 1 and prints why. The Pod declares it as the container's readiness probe and liveness probe, so the kubelet reports the answer on the Pod, and nothing dials the container.
- **A failed start** exits 1 and writes `<Reason>: <message>` to `--termination-log`, `/dev/termination-log` by default, which the kubelet reports as the container's termination message. `Reason` is `BotInvalid` or `Failed`, the second for a start the first does not name, such as a `--connector-url` without its files.
- **How a manager reads it.** A Bot's `Running` condition (interface 1 STA-1) is `True` while the container is ready and `False` otherwise. While the container is not ready, the condition's reason is the termination message's reason where the container last terminated, and `Starting` otherwise. Writing the condition is the manager's.

### What the image asks of the Pod

- **User.** It runs as uid and gid 65532 (`build/runtime.Dockerfile`), named by number in the image so the kubelet can verify it, with no capability. So the Pod can assert `runAsNonRoot` without a `runAsUser`, drop every capability, forbid privilege escalation and use the RuntimeDefault seccomp profile, which is what PodSecurity `restricted` asks.
- **Status directory.** `/run/garam-bot` is writable by that user in the image. A Pod with a read-only root filesystem mounts an `emptyDir` there.
- **Files.** The Bot file, the connector's trust file and the credential files are mounted read-only, and readable by uid 65532 or the Pod's group. At a cluster the credential is the Bot's projected ServiceAccount token (garam-bot-operator ADR 0097).
- **Network.** It reaches its connector, at a cluster a Service in the release namespace.
- **Image.** It is `garam-bot-runtime:<revision>` from `make build-images`, published to `garam-bot/runtime` and referenced by digest (`deployment.md` §Images and tags).

## Rationale

- ADR 0033: why a separate binary, why an exec probe over an endpoint, and the image's shape.
- ADR 0042: why the process holds the key its token is bound to, and obtains the token from its connector rather than from a file.
- ADR 0032: the file form it reads.
- ADR 0043: why it reaches its workspace through its connector, and how it drives interface 2.

## Open questions

- **Several bots in one process** (one pod one PC, ADR 0031), which waits for a runtime that runs them.
- **A stale or expiring token as its own condition**, rather than only in the status file.
