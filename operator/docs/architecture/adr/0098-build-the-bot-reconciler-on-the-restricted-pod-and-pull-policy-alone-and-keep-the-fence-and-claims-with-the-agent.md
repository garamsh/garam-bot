# ADR 0098: Build the Bot reconciler on the restricted Pod and the pull policy alone, and keep the fence and claims with the Agent

> Status: accepted; decision 3's cluster-owned Bot reaching no connector superseded by ADR-0099
> Date: 2026-11-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- ADR 0090 decision 3 builds the Bot reconciler from the Agent workload code. What the two share moves out of `internal/controller` into a package below both. It names the shared parts:
  - the StatefulSet of one with its claims;
  - the credential Secret projected to the one container that holds garam tokens;
  - the writer fence;
  - the restricted security context;
  - pull-always, and the grace period.
- Three of those have nothing to act on for a Bot today:
  - **Claims and the fence.** The garam-bot runtime keeps no state (garam-bot `runtime.md`), so a Bot's Pod mounts no claim, and there is no single writer of one to fence.
  - **The credential Secret.** ADR 0097 replaced it: the runtime obtains its token from the connector, on its projected ServiceAccount token.
  - **Workspaces.** They are interface 1's own kind, and not this reconciler's.
- **The grace period.** The Agent's comes from its type's descriptor, sized to the agent's drain (ADR 0064). The runtime stops at SIGTERM and has nothing to drain.
- garam-bot#1254 asked for the reconciler. The PM approved this narrowing on 2026-11-28, before it was built.

## Decision

1. **`internal/workload` holds what both reconcilers' Pods carry, and nothing else.** It is the fields PodSecurity `restricted` reads, and the pull policy:
   - `ContainerSecurityContext`;
   - `RestrictPod`, the Pod-wide user, group, `runAsNonRoot` and the RuntimeDefault seccomp profile;
   - `PullPolicy`, `Always`.

   The Agent reconciler builds its StatefulSet and its memory move's Jobs through them, and its envtest suite passes unchanged. The package imports no domain (`structure.md` §Shared code).
2. **The writer fence, the claims and the credential Secret stay in `internal/controller`'s Agent files.** They move below both reconcilers when a Bot first holds state or a Secret. That change makes the extraction this ADR declines.
3. **The Bot reconciler is `internal/controller/bot_controller.go`, with `bot_workload.go` and `bot_status.go`.** For each Bot of a namespace claimed for its release, it builds four objects. The Bot is the controller of every one, so deleting the Bot deletes them:
   - **A ServiceAccount of the Bot's name.** It holds no rights, and `automountServiceAccountToken` is false.
   - **The Bot file.** The ConfigMap `<bot>-bot` holds the Bot in interface 1's file form under `bot.yaml`.
   - **The trust copy.** The ConfigMap `<bot>-connector-trust` holds the connector's trust bundle, copied from `--trust-configmap` in `--release-namespace` under `ca.crt`. Every change of the source is copied again.
   - **The StatefulSet of one.** It runs `--bot-image`:
     - `run` with `--bot-file`, `--connector-url`, `--connector-trust-file` and `--credential-file`;
     - the ServiceAccount token projected with the audience `--bot-token-audience` for 600 seconds;
     - `/garam-bot probe` as its readiness and liveness probes;
     - the restricted context of decision 1, as the runtime's user, 65532.
   - **A cluster-owned Bot reaches no connector.** It has no garam identity, so its Pod gets no trust copy, no projected token and no connector flags, and its runtime runs with none (garam-bot `runtime.md`). The Bot's ownership marker decides only this. The connector still checks every Bot it answers against what it applied itself (ADR 0097).
4. **A Bot's `Running` condition is read off its Pod**, as garam-bot `runtime.md` §The status contract says. It is `True` while the runtime's container is ready. It is `False` otherwise, with the reason the runtime's termination message names, or `Starting` before any. A garam-owned Bot is `WaitingForConnectorTrust`, with no Pod built, while the connector has published no bundle. Only the Pod the Bot's own StatefulSet controls is read.
5. **In a namespace whose claim does not name `<release-namespace>/<release-name>`, the reconciler writes nothing**, the Bot's status included, and logs once per Bot why. A claim's change wakes the namespace's Bots.
6. **The manager starts each reconciler whose kind the API server serves** (ADR 0090 decision 4). It logs the kinds it found and the reconcilers it started, and refuses to start where `--control-address` is set and `Agent` is not served. Where `--watch-namespaces` is set, the cache holds those namespaces and `--release-namespace` alone.

## Consequences

- ADR 0090 decision 3 is narrowed to decision 1 here until a Bot holds state.
- The manager's ClusterRole gains:
  - `get`, `list` and `watch` on Bots, and `update` on `bots/status`;
  - `create` and `update` on ServiceAccounts and ConfigMaps;
  - `get`, `list` and `watch` on Leases.

  The chart (garam-bot#1253) grants them as Roles in the watched namespaces, with `get`, `list` and `watch` on the trust ConfigMap in the release namespace.
- The manager gains seven flags: `--bot-image`, `--watch-namespaces`, `--release-namespace`, `--release-name`, `--connector-url`, `--trust-configmap` and `--bot-token-audience` (`configuration.md`). It needs no garam address or key for a Bot.
- A Bot and an Agent of one name in one namespace would claim one StatefulSet name. The chart installs no `Agent` kind, and our own system installs no Bot kind, so neither cluster holds both.

## Signs the decision was wrong

- A Bot needs a volume that outlives its Pod before the fence moves below both reconcilers.
- A second change to the Agent's or the Bot's Pod shape finds itself editing both reconcilers for one rule that `internal/workload` should hold.

## Rejected alternatives

- **Extracting the fence and the claims now.** They are about 1,500 lines that only the Agent reaches. Moving them below both would add a seam with one caller, which `simplicity.md` refuses.
- **One trust copy per namespace, shared by its Bots.** An object has one controller, so a shared copy could not be owned by each Bot it serves. Deleting the last Bot would then leave it behind.
- **The Bot's status written in an unclaimed namespace.** That is a write in a namespace another release holds, which ADR 0091 decision 1 refuses.
