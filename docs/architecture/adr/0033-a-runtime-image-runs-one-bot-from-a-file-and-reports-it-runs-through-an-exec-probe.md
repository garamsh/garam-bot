# ADR 0033: A runtime image runs one Bot from a file, and reports that it runs through an exec probe

> Status: accepted
> Date: 2026-11-24

## Context

- `garamsh/garam-bot-operator` goal #436 needs from garam-bot a runtime that runs one Bot, and a way for its manager to learn that the bot's process runs (#1198, #1199). That repository decided the following:
  - **One StatefulSet of one.** One Bot is one StatefulSet of one, whose runtime reads its Bot from a mounted file with the bot's tokens beside it (its ADR 0090 decision 3).
  - **Images by digest.** Every image of its chart is pulled by digest, and a PodSecurity `restricted` namespace admits it (ADR 0091 decision 7).
  - **No inbound path.** A NetworkPolicy admits no ingress to any Pod, and the manager holds read rights on Pods and write rights on interface-1 status, nothing that reaches into a container (ADR 0091 decisions 2 and 4).
- Interface 1 v1alpha1 (ADR 0032) gives the file form, `manifest.Parse`, and the Bot condition `Running` (STA-1).
- ADR 0031 supersedes the in-house loop of the `sherlock` binary. The agent harness, workspace provider and control connection (interfaces 2 to 4) are not built, so a Bot has no work source yet.
- A garam-owned bot's token is bound to a key its own process holds, and the process obtains it from its place's connector (ADR 0042). A cluster-owned bot has no garam identity yet.

## Decision

1. **A new binary, `garam-bot`, separate from `sherlock`.**
   - **`garam-bot run`** reads one Bot from `--bot-file` with `manifest.Parse`, and refuses anything but a Bot. With `--connector-url`, it obtains the bot's garam token from its place's connector and obtains it again before it expires, as ADR 0042 decides. An attempt that fails keeps the token held and marks it stale. Without the flag, the bot runs with no garam identity. The process holds the Bot until SIGTERM and then exits 0.
   - **The domain is `internal/bot/`.**
2. **The status contract is a status file and an exec probe.**
   - **The status file.** `run` rewrites a JSON status file, `--status-file`, every 5 seconds. The file holds the state (`Running` or `Stopped`), a heartbeat, the Bot's namespace, name and generation, and, with a connector, when the token held was obtained, when it expires and whether the latest attempt failed.
   - **The probe.** `garam-bot probe` exits 0 only while the state is `Running` and the heartbeat is at most 30 seconds old.
   - **How the Pod uses it.** The Pod declares the probe as its readiness and liveness probe, so the kubelet reports it on the Pod and no network path to the container is needed.
   - **A failed start** exits 1 and writes `<Reason>: <message>` to `--termination-log`, the container's termination message. The reasons are `BotInvalid` and `Failed`.
   - **Not the states #1199 proposed.** It proposed `Starting`, `Running` and `TokenUnreadable` as states of the runtime. The file holds only what the process itself knows: before the file is written there is no process to report `Starting`, so the manager derives it from a container not yet ready, and a token that cannot be obtained again leaves the bot running, so it is the `stale` mark and not a state.
3. **The runtime image is `build/runtime.Dockerfile`.** It is distroless static, running as the numeric user 65532:65532, so a Pod that asserts `runAsNonRoot` needs no `runAsUser`, with the binary at `/garam-bot` and `/run/garam-bot` writable by that user. It is built by `make build-images` as `garam-bot-runtime:<revision>`, and published by the release workflow to the ECR repository `garam-bot/runtime`, whose digest it records.

## Consequences

- garam-bot-operator's Bot reconciler mounts the Bot file, and for a garam-owned Bot the connector's trust and the Bot's projected ServiceAccount token (its ADR 0097). It mounts a writable volume at the status file's directory, and declares `garam-bot probe` as the container's probes. It maps the container's readiness to `Running`, and the termination message's reason to the condition's reason.
- A stale token does not make a bot stop running. The status file reports it, and a later contract may surface it as its own condition.
- Publishing needs the ECR repository `garam-bot/runtime` and the release role's push grant on it, which garamsh/aurora holds. The release workflow reads each repository before any push and pushes the runtime image first. A missing repository or a refused read then stops the run before anything is published, and a refused push stops it before the `sherlock` repositories take the version, which no re-push repairs (`deployment.md` §Images and tags).
- Several bots in one process, and the work a bot does, wait for interfaces 2 to 4.

## Signs the decision was wrong

- The kubelet's probe period and the 30-second bound disagree in practice, so a healthy bot flaps between ready and not ready.
- A manager needs more of the process's state than ready or not and a reason, such as progress or errors of work, which an exec probe cannot carry. That would point to a status the runtime writes to the API itself.
- The runtime needs rights in the cluster, which would put it on the other side of ADR 0091's boundary.
