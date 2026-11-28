# ADR 0099: Serve every Bot's process its workspace from the connector, in one dedicated workspace namespace, also with garam off

> Status: accepted
> Date: 2026-11-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- garam-bot's ADR 0043 makes a bot's workspace interface 7, `api/workspace-access/v1alpha1/`. The bot's process calls its place's connector, and the connector links the workspace providers and holds their rights. This record decides the cluster's side.
- **What the connector holds today:**
  - interface 6, served over TLS it makes itself, on the Bot's projected ServiceAccount token (ADR 0097);
  - the naru Machine provider, where `--naru-url` is set (garam-bot#1275). One naru caller sees every Machine it made, whichever release made it.
- **Who it serves today.** It serves garam-owned Bots alone, and it starts only with garam configured: `--garam-url`, `--garam-root-file` and `--registration-token-file` are required.
- **The standalone mode.** ADR 0090 decision 1 makes the chart with the connector turned off the standalone cluster mode. ADR 0098 decision 3 gives a cluster-owned Bot no trust copy, no projected token and no connector flags.
- **The Pod provider's rights.** It needs Pods, claims, NetworkPolicies, `pods/exec` and `pods/portforward` in the namespace its workspaces live in (garam-bot `interfaces.md`).
  - **In a watched namespace,** `pods/exec` would reach the bots' own Pods, whose processes hold the bots' garam tokens in memory.
  - **A right to create Pods** where Pod Security does not hold them can make a privileged or a host-path Pod.
  - **Its egress capability.** It enforces egress only where the cluster's network plugin enforces NetworkPolicy, which the Kubernetes API cannot say, so `Capabilities.Egress` is the place's setting.
- **A workspace's data volume is what deleting it destroys.** A deletion decided from an incomplete view, such as a narrowed configuration, another release's objects or a cache that has not seen a Bot, destroys a bot's data that is not this connector's to destroy.
- The PM approved this design on garam-bot#1281 on 2026-11-28. The PM's review of garam-bot#1283 then decided:
  - the deletion rule;
  - the dial bounds;
  - the per-instance request limit;
  - the instance's container;
  - the per-provider egress;
  - the namespace's hardening;
  - the stub issuer's build tag.

## Decision

1. **The connector serves interface 7 on the listener, Service and certificate of interface 6.**
   - **Who is served.** Every Bot in a namespace claimed for its release, garam-owned or cluster-owned.
   - **The Bot's resolution.** A Bot is resolved from its credential as ADR 0097 decision 2 resolves it, on its own audience, `--workspace-audience`, `garam-workspace` by default. The token must name a live Pod of the StatefulSet whose controller is the Bot, by UID, under the Bot's ServiceAccount. A workload that is no Bot's, or a Bot of a namespace not claimed for the release, is `unknown_bot`.
   - **Interface 6 does not narrow it.** Interface 6's "applied by the connector itself" is not required here, because a cluster-owned Bot has a workspace too. Interface 6 keeps its own resolution, so a cluster-owned Bot still obtains no garam token.
2. **The credential is reused while it lives, and the key is pinned per workload instance.**
   - **The instance.** It is the Pod's UID with its `runtime` container's `containerID`, read from `status.containerStatuses` while that container's state is `running`. While the container is not running, the Pod is no running instance and its credential is refused.
   - **The binding cache.** A credential's binding to its instance is kept for 10 seconds at most, so the API server is not read for every request. Before answering `invalid_key`, the connector reads the Pod again from the API server, so a restart the cache has not yet seen is not refused.
   - **The pin.** The first key whose proof verifies for an instance is pinned to it. Every request of that instance under another key is answered 401 `invalid_key`, the one answer for that case.
   - **A restarted container** has a new ID, so its new process pins its own key.
   - **Memory alone.** Pins are held in memory, and the pin of a Pod that no longer exists is dropped.
   - **Across a connector restart.** As with interface 6's spent set, a credential issued before the connector started is refused, and a pin is never persisted. The connector runs one replica (ADR 0097 decision 6), which PIN-1 requires.
3. **The connector runs with garam off.**
   - **What changes.** With `--garam-url` unset, the connector registers nothing, polls no declarations, sends no reports, and answers every interface 6 call 403 `unknown_bot`. It serves interface 7 and runs the providers, and the chart renders no egress to garam.
   - **What it requires.** `--release-namespace`, `--release-name` and `--watch-namespaces` stay required. The garam flags are required together or not at all.
   - **The standalone mode.** This supersedes ADR 0090 decision 1's standalone mode. The standalone cluster mode is now the chart with garam off: the manager and the connector both run, and nothing reaches garam.
4. **The workspaces live in one dedicated namespace, `--workspace-namespace`, which the chart renders.**
   - **Never watched.** The chart refuses a value that is also a watched namespace or the release namespace. A Bot's workspace is never in the Bot's own namespace.
   - **One release.** The chart renders the Namespace itself, so Helm refuses a second release that renders the same one. The Namespace carries the label `bot.garam.sh/workspace-release: <release-namespace>.<release-name>`. A namespace made outside the chart cannot be named, so no sharing Helm does not see is possible. The sweep's release labels (decision 6) keep even a shared one's workspaces apart.
   - **A third namespace of rights, and a second cluster-scoped object.** This widens ADR 0091 decision 1: the chart's runtime Roles are in the release namespace, the watched namespaces and this one, and still no ClusterRole. It also widens ADR 0091 decision 3: CRDs and this Namespace are the cluster-scoped objects the chart renders, both with the installer's rights.
   - **Pod Security.** The Namespace is labelled `pod-security.kubernetes.io/enforce: restricted`. The Pod provider's Pods, its file helper's init container included, already run non-root with every capability dropped, no privilege escalation and the RuntimeDefault seccomp profile, so `restricted` admits them, and `baseline` is not needed.
   - **The default ServiceAccount.** The connector sets `automountServiceAccountToken: false` on the namespace's `default` ServiceAccount at start, by `get` and `update` on that one ServiceAccount by `resourceNames`. It serves no workspace until that is done. Helm cannot own an object the cluster's controller creates, so the chart does not render it.
   - **The Pod provider.** It moves to garam-bot's `pkg/podworkspace` and runs in the connector, rooted in that namespace. Its rights are there alone: Pods, PersistentVolumeClaims, NetworkPolicies, `pods/exec`, `pods/portforward` and VolumeSnapshots, as garam-bot `interfaces.md` lists them. The connector holds no exec, port-forward or Pod-creating right in a watched namespace, so it cannot reach a bot's process.
   - **The naru Machine provider** stays as it runs.
   - **Choosing a provider.** A WorkspaceClass's `spec.provider` names one, `pod` or `naru`. A class naming a provider the connector does not run is answered `unsupported` by acquire.
5. **Each workspace is named per Bot and per release, and isolated.**
   - **Naming.** Its name is `w<release>-<bot>`: `<release>` is the first 12 hex digits of the SHA-256 of `<release-namespace>/<release-name>`, and `<bot>` the first 40 of the SHA-256 of the Bot's namespace, name and UID and the attachment's name. A request names only the attachment. For naru the `w<release>-` prefix is what marks this release's Machines.
   - **Labels.** Each workspace carries the labels `bot.garam.sh/workspace-release-namespace`, `bot.garam.sh/workspace-release-name`, `bot.garam.sh/bot-namespace`, `bot.garam.sh/bot-name`, `bot.garam.sh/bot-uid` and `bot.garam.sh/attachment`. They reach the backend through interface 3's optional `Spec.Labels`, which garam-bot ADR 0043 adds, and are read back from it.
   - **Ingress.** The Pod provider gives each workspace a NetworkPolicy admitting no ingress at all. `Dial` uses the `portforward` subresource, which reaches the Pod through the kubelet rather than the Pod network, so it is the one way in.
   - **Egress capability.** The Pod provider's `Capabilities.Egress` is `--pod-workspace-enforces-egress`, the chart value `workspaces.pod.enforcesNetworkPolicy`, true by default. The chart's documentation names its requirement: a network plugin that enforces NetworkPolicy egress. Where the value is false, a workspace with egress rules is refused, and so every Pod workspace is.
   - **A naru Machine's isolation.** For a naru Machine, `Dial` goes through naru-proxy, which serves a Machine's services to whoever reaches it. So only the adapter's token, interface 2's AUTH-1, guards its port (garam-bot ADR 0034).
6. **The deletion sweep touches this release's workspaces alone, and deletes on evidence only.**
   - **What it reads.** It lists each provider's workspaces and takes only those with this release's name prefix and release labels. A workspace without them is never touched.
   - **A namespace no longer watched.** A workspace whose `bot-namespace` label names a namespace this release no longer watches, or no longer claims, is left alone.
   - **When it deletes.** It deletes a workspace only after both:
     - **A live check.** A `GET` of the Bot from the API server, by its labels' namespace and name, answers NotFound or a UID other than the label's.
     - **A grace.** 10 minutes have passed since the sweep first found it so.

     The first finding is held in memory, so a restart starts the grace again.
   - **No live Bot is presumed absent from a cache.**
7. **Workspace egress is per provider, as addresses the place gives.**
   - **The URLs.** The model gateway and the tool proxy are `--pod-model-gateway-url` and `--pod-tool-proxy-url` for Pod workspaces, and `--naru-model-gateway-url` and `--naru-tool-proxy-url` for naru Machines. A naru Machine runs outside the cluster, so its URLs are ones it reaches from there.
   - **The egress.** It is `--pod-workspace-egress` and `--naru-workspace-egress`, each a list of `<cidr>/<protocol>/<port>`. The chart values `workspaces.pod.egress` and `workspaces.naru.egress` render them. The chart refuses a list without a rule to port 53, so a workspace resolves its URLs' names.
   - **No derived ranges.** A rule names Pod or host addresses after DNAT, never a Service's address, and the connector derives no range from a URL.
   - **The connector's own egress** gains its issuers', the model gateway's and the tool proxy's addresses, `connector.issuers.egress`, for TOK-1. With garam off it has none to garam.
8. **The images come from the chart.**
   - **The workspace image.** It is `--agent-image <harness>=<repository>@sha256:<digest>`, one per harness, so a Bot's `spec.agent.harness` picks it and its declaration names none (ADR 0091 decision 6). A harness with no image is answered `unsupported` by acquire.
   - **The file helper's image** for the Pod provider is `--workspace-helper-image`.
9. **The session's tokens come through an issuer the connector is given, and the released connector has none.**
   - **Before G3 and G6** there is no production issuer, so the session tokens call answers 503 `waiting_for_gateway`.
   - **The stub issuer.** It is compiled only under the build tag `e2e`, which no release build sets. The end-to-end suite builds its own connector image with it (garam-bot#1252). So no flag or chart value of a released connector can wire it.
10. **Limits.**
    - **Per Bot.** Each Bot has a bucket of 20 calls that reach a provider or an issuer, refilled one every 3 seconds: acquire, a write of files, a dial and session tokens. A read of the workspace is answered from the provider's record and takes nothing.
      - A renewal is one session tokens call and one write of up to 16 files.
      - A call past the bucket is answered 429 `temporarily_unavailable` with `Retry-After`, to that Bot's process alone.
      - A call refused before a provider is asked takes nothing.
    - **Per instance.** Each workload instance has a bucket of 30 requests, refilled one a second. It is taken right after the credential verifies against the keys the connector holds, before any Pod is read, refused requests included.
    - **Dials.**
      - At most 4 are open per instance, a fifth answered 429.
      - At most `--max-dials`, 256 by default, are open in all, the next answered 503.
      - A dial idle for 120 seconds is closed.
      - An instance's dials are closed when its Pod or container ends, when its Bot is deleted or made again, and when the workspace stops or is deleted.
11. **What is logged.** Each call is logged with the Bot's namespace and name, the call and its outcome. File content, a token, the credential and the proof are never logged, nor what passes through a dial.
12. **The manager gives every Bot the workspace credential and the trust bundle.**
    - **Every Bot.** Each one's Pod gets:
      - the trust copy;
      - a second projected token with the audience `--workspace-audience` for 600 seconds;
      - `--connector-url`, `--connector-trust-file` and `--workspace-credential-file`.
    - **A garam-owned Bot alone** also gets interface 6's token and `--credential-file`.
    - **What this supersedes.** ADR 0098 decision 3's "A cluster-owned Bot reaches no connector". A cluster-owned Bot reaches its connector for its workspace, and still has no garam identity.
    - **Waiting for trust.** `WaitingForConnectorTrust` applies to every Bot.

## Consequences

- **The connector's rights.**
  - **In the workspace namespace,** its Role grants the Pod provider's rights, and `get` and `update` on the `default` ServiceAccount by name.
  - **In the watched namespaces,** its Role gains `get` on Bots, for the sweep's live check. It reads Bots and WorkspaceClasses otherwise from its cache, which its `list` and `watch` on the four kinds allow, and Pods and StatefulSets by `get`, as before.
- **The chart renders:**
  - the workspace Namespace with its release and Pod Security labels, and its Role and RoleBinding;
  - the refusal of an overlapping value, and of an egress list without DNS;
  - the connector's ingress unchanged, one port from the watched namespaces;
  - the connector's egress to its issuers, and none to garam when garam is off.

  `test/chart` fails a render without the Namespace's labels, and an installed chart whose workspace namespace's `default` ServiceAccount still mounts its token once the connector is ready.
- **The manager's Bot reconciler** renders the trust copy and the workspace token for every Bot. Its `WaitingForConnectorTrust` now holds a cluster-owned Bot too, so a chart with the connector removed runs no Bot's workspace.
- **A connector restart** breaks every dial. The processes dial again once their credentials are replaced, within 8 minutes, and the adapters keep their sessions (garam-bot ADR 0043).
- **The namespace's writers.** Whoever can write workloads in the workspace namespace can reach every workspace, so it is the connector's alone, and the chart's documentation says so.
- **A deleted Bot's data** outlives it by at least 10 minutes, and longer where its namespace stops being watched, which the operator then cleans by hand.

## Signs the decision was wrong

- **Users ask for workspaces beside their Bots,** because a dedicated namespace does not fit their quotas or policies.
- **A connector restart leaves bots idle for minutes** often enough to be seen.
- **The pin refuses a rightful process.** A process restarting in the same container, without a new container ID, is refused by its pin.
- **The limits are too small.** A Bot's normal course is answered 429, or four dials are too few for one process.
- **Workspaces of deleted Bots pile up,** because the sweep's evidence rule leaves too much behind.

## Rejected alternatives

- **The bot's process links the providers.** It spreads `pods/exec` or the naru caller certificate to every Bot, against ADR 0091's least privilege (garam-bot ADR 0043's context).
- **The workspaces in the watched namespaces.** The Pod provider's `pods/exec` there would reach the bots' own Pods.
- **A pin per Pod UID alone.** A restarted container makes a new key, and would be refused for the Pod's whole life.
- **A sweep of every workspace no Bot in the cache names.** It deletes another release's workspaces, every naru Machine of the caller, and a narrowed configuration's.
- **One gateway URL for every provider.** A naru Machine cannot reach a ClusterIP, and a Pod need not reach what a Machine reaches.
- **The stub issuer behind a flag.** Any installation could then issue tokens no gateway checks.
- **The manager serving interface 7.** It opens no listener, and ADR 0090 keeps it to the Kubernetes API.
- **A connector that stays off without garam.** A cluster-owned Bot would then have no workspace, and the standalone mode would run no bot's work.
