# ADR 0099: Serve every Bot's process its workspace from the connector, in one dedicated workspace namespace, also with garam off

> Status: accepted
> Date: 2026-11-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- garam-bot's ADR 0043 makes a bot's workspace interface 7, `api/workspace-access/v1alpha1/`. The bot's process calls its place's connector, and the connector links the workspace providers and holds their rights. This record decides the cluster's side.
- **What the connector holds today:**
  - interface 6, served over TLS it makes itself, on the Bot's projected ServiceAccount token (ADR 0097);
  - the naru Machine provider, where `--naru-url` is set (garam-bot#1275).
- **Who it serves today.** It serves garam-owned Bots alone, and it starts only with garam configured: `--garam-url`, `--garam-root-file` and `--registration-token-file` are required.
- **The standalone mode.** ADR 0090 decision 1 makes the chart with the connector turned off the standalone cluster mode. ADR 0098 decision 3 gives a cluster-owned Bot no trust copy, no projected token and no connector flags.
- **The Pod provider's rights.** It needs Pods, claims, NetworkPolicies, `pods/exec` and `pods/portforward` in the namespace its workspaces live in (garam-bot `interfaces.md`). Held in a watched namespace, `pods/exec` would reach the bots' own Pods, whose processes hold the bots' garam tokens in memory.
- The PM approved this design on garam-bot#1281 on 2026-11-28. The approval asked for:
  - one dedicated workspace namespace, never a watched one;
  - each workspace isolated by its own NetworkPolicy and named per Bot;
  - a second key for one Pod refused with one answer;
  - per-Bot limits as interface 6 has.

## Decision

1. **The connector serves interface 7 on the listener, Service and certificate of interface 6.**
   - **Who is served.** Every Bot in a namespace claimed for its release, garam-owned or cluster-owned.
   - **The Bot's resolution.** A Bot is resolved from its credential as ADR 0097 decision 2 resolves it, on its own audience, `--workspace-audience`, `garam-workspace` by default. The token must name a live Pod of the StatefulSet whose controller is the Bot, by UID, under the Bot's ServiceAccount.
   - **Interface 6 does not narrow it.** Interface 6's "applied by the connector itself" is not required here, because a cluster-owned Bot has a workspace too. Interface 6 keeps its own resolution, so a cluster-owned Bot still obtains no garam token.
2. **The credential is reused while it lives, and the key is pinned per workload instance.**
   - **The instance.** It is the Pod's UID with its runtime container's ID, both read from the API server, not a cache.
   - **The pin.** The first key whose proof verifies for an instance is pinned to it. Every request of that instance under another key is answered 401 `invalid_key`, the one answer for that case.
   - **A restarted container** has a new ID, so its new process pins its own key. Pins are held in memory alone.
   - **Across a connector restart.** As with interface 6's spent set, a credential issued before the connector started is refused, and a pin is never persisted.
3. **The connector runs with garam off.**
   - **What changes.** With `--garam-url` unset, the connector registers nothing, polls no declarations, sends no reports, and answers every interface 6 call 403 `unknown_bot`. It serves interface 7 and runs the providers.
   - **What it requires.** `--release-namespace`, `--release-name` and `--watch-namespaces` stay required. The garam flags are required together or not at all.
   - **The standalone mode.** This supersedes ADR 0090 decision 1's standalone mode. The standalone cluster mode is now the chart with garam off: the manager and the connector both run, and nothing reaches garam.
4. **The workspaces live in one dedicated namespace, `--workspace-namespace`.**
   - **A third namespace of rights.** This widens ADR 0091 decision 1: the chart's runtime Roles are in the release namespace, the watched namespaces and this one, and still no ClusterRole.
   - **Never watched.** The chart refuses a value that is also a watched namespace or the release namespace. A Bot's workspace is never in the Bot's own namespace.
   - **The Pod provider.** It moves to garam-bot's `pkg/podworkspace` and runs in the connector, rooted in that namespace. Its rights are there alone: Pods, PersistentVolumeClaims, NetworkPolicies, `pods/exec`, `pods/portforward` and VolumeSnapshots, as garam-bot `interfaces.md` lists them.
   - **What it cannot reach.** The connector holds no exec, port-forward or Pod-creating right in a watched namespace, so it cannot reach a bot's process.
   - **The naru Machine provider** stays as it runs.
   - **Choosing a provider.** A WorkspaceClass's `spec.provider` names one, `pod` or `naru`. A class naming a provider the connector does not run is answered with a reason by acquire.
5. **Each workspace is named per Bot and isolated.**
   - **Naming.** Its name is a hash of the Bot's namespace, name and UID and the attachment's name. A request names only the attachment.
   - **The NetworkPolicy.** The Pod provider gives each workspace a NetworkPolicy admitting no ingress at all. `Dial` uses the `portforward` subresource, which reaches the Pod through the kubelet rather than the Pod network, so it is the one way in.
   - **Egress** is the rules the connector builds: the place's model gateway and tool proxy, from `--model-gateway-url` and `--tool-proxy-url` and their ranges.
   - **Deletion.** The connector deletes every workspace in the namespace whose name no live Bot's attachment yields, once its cache of Bots has synced. The namespace is the connector's alone, so nothing else is in reach.
6. **The images come from the chart.**
   - **The workspace image.** It is `--agent-image <harness>=<repository>@sha256:<digest>`, one per harness, so a Bot's `spec.agent.harness` picks it and its declaration names none (ADR 0091 decision 6). A harness with no image is answered with a reason by acquire.
   - **The file helper's image** for the Pod provider is `--workspace-helper-image`.
7. **The session's tokens come through an issuer the connector is given.**
   - **Before G3 and G6** there is no production issuer. The chart sets none, so the session tokens call answers 503 `waiting_for_gateway`.
   - **The end-to-end suite** wires a stub issuer and a stub gateway (garam-bot#1252).
8. **Each Bot's calls that reach a provider are limited.** Each Bot has a bucket of 20, refilled one every 3 seconds. A call past it is answered 429 `temporarily_unavailable` with `Retry-After`, to that Bot's process alone. A call refused before a provider is asked takes nothing.
9. **The manager gives every Bot the workspace credential and the trust bundle.**
   - **Every Bot.** Each one's Pod gets:
     - the trust copy;
     - a second projected token with the audience `--workspace-audience` for 600 seconds;
     - `--connector-url`, `--connector-trust-file` and `--workspace-credential-file`.
   - **A garam-owned Bot alone** also gets interface 6's token and `--credential-file`.
   - **What this supersedes.** ADR 0098 decision 3's "A cluster-owned Bot reaches no connector". A cluster-owned Bot reaches its connector for its workspace, and still has no garam identity.
   - **Waiting for trust.** `WaitingForConnectorTrust` applies to every Bot.
10. **What is logged.** Each call is logged with the Bot's namespace and name, the call and its outcome. File content, a token, the credential and the proof are never logged, nor what passes through a `Dial`.

## Consequences

- **The connector's Role in the workspace namespace** grants the Pod provider's rights. Its Role in the watched namespaces is unchanged: it reads Bots and WorkspaceClasses from its cache, which its `list` and `watch` on the four kinds allow, and Pods and StatefulSets by `get`.
- **The chart renders:**
  - the workspace namespace's Role and RoleBinding;
  - the refusal of an overlapping value;
  - the connector's ingress unchanged, one port from the watched namespaces;
  - no ingress to any workspace.
- **The manager's Bot reconciler** renders the trust copy and the workspace token for every Bot. Its `WaitingForConnectorTrust` now holds a cluster-owned Bot too, so a chart with the connector removed runs no Bot's workspace.
- **A connector restart** breaks every `Dial`. The processes dial again once their credentials are replaced, within 8 minutes, and the adapters keep their sessions (garam-bot ADR 0043).
- **The namespace's writers.** Whoever can write workloads in the workspace namespace can reach every workspace, so it is the connector's alone, and the chart's documentation says so.

## Signs the decision was wrong

- **Users ask for workspaces beside their Bots,** because a dedicated namespace does not fit their quotas or policies.
- **A connector restart leaves bots idle for minutes** often enough to be seen.
- **The pin refuses a rightful process.** A process restarting in the same container, without a new container ID, is refused by its pin.
- **The bucket is too small.** A Bot's normal course is answered 429.

## Rejected alternatives

- **The bot's process links the providers.** It spreads `pods/exec` or the naru caller certificate to every Bot, against ADR 0091's least privilege (garam-bot ADR 0043's context).
- **The workspaces in the watched namespaces.** The Pod provider's `pods/exec` there would reach the bots' own Pods.
- **A pin per Pod UID alone.** A restarted container makes a new key, and would be refused for the Pod's whole life.
- **The manager serving interface 7.** It opens no listener, and ADR 0090 keeps it to the Kubernetes API.
- **A connector that stays off without garam.** A cluster-owned Bot would then have no workspace, and the standalone mode would run no bot's work.
