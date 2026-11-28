# Connector

How a user's own cluster is connected to garam: the chart a user installs, the connector program in it, its registration and identity, the declarations it syncs both ways, the rights it holds in the cluster, and what garam owes on its side.

Registration and identity are built, in `cmd/connector` and `internal/clusteridentity` (#440), except registering again after a key is lost (#450). Applying declarations and reporting are built, in `internal/declaration` (#441, #442). Bots' tokens are built, in `internal/bottoken` (garam-bot#1255). The manager's Bot reconciler is built, in `internal/controller` (garam-bot#1254). The naru Machine provider runs in the connector where the chart turns it on (garam-bot#1209). The bots' workspaces, interface 7, are built in `internal/workspaceaccess`, the chart renders their namespace, rights and values, the connector serves them with garam off, and the manager gives every Bot its workspace credential (garam-bot#1281). The rest is not built yet. This document states what is decided. Goal #436 lists the implementation issues, and each section names the decision it holds.

## Current decisions

### The chart and its two programs

- **A user connects a cluster by installing the connector chart in it**, in EKS or any conformant cluster ([ADR 0087](adr/0087-connect-a-users-cluster-through-a-connector-it-installs-that-dials-garam-over-https-long-poll.md)). The chart is a product users install: for it, ADR 0079's "runs only on our own system" and "BYO on hold" no longer hold.
- **The chart, `deploy/charts/garam-connector/`, deploys two programs as two Deployments from two images** ([ADR 0090](adr/0090-run-the-connector-as-a-binary-beside-the-manager-and-build-the-bot-reconciler-from-the-agent-workload.md)):
  - **The manager** reconciles garam-bot's interface-1 objects into workloads. It needs no garam connection.
  - **The connector**, `cmd/connector`, holds the cluster's identity and is the only program that talks to garam. It also serves every Bot's workspace (ADR 0099), so the cluster mode that works without garam is the chart with garam off, not with the connector off: the connector then registers nothing and serves the workspaces alone.
  - **They meet only through the Kubernetes API.**
- **The Bot reconciler is built from the Agent workload code** ([ADR 0098](adr/0098-build-the-bot-reconciler-on-the-restricted-pod-and-pull-policy-alone-and-keep-the-fence-and-claims-with-the-agent.md)). What the two share today, the restricted Pod and the pull policy, is `internal/workload`. The writer fence and the claims stay with the Agent until a Bot holds state. One Bot is one StatefulSet of one, whose runtime reads that Bot from a mounted file.
- **What the manager builds for a Bot**, in a namespace whose claim names its release, every object controlled by the Bot:
  - a ServiceAccount of the Bot's name, with no rights and no token mounted but the connector's;
  - the Bot file, the ConfigMap `<bot>-bot`;
  - the trust copy, the ConfigMap `<bot>-connector-trust`, copied again on every change of `--trust-configmap`;
  - the StatefulSet of one, running `--bot-image`, with `garam-bot probe` as its probes, and PodSecurity `restricted` admits its Pod.
  - **Every Bot, garam-owned or cluster-owned** (ADR 0099 decision 12), gets the trust copy, a ServiceAccount token projected for `--workspace-audience`, `garam-workspace`, 600 seconds, and `--connector-url`, `--connector-trust-file` and `--workspace-credential-file`, so it reaches its workspace through the connector.
  - **Only a garam-owned Bot** also gets the token projected for `--bot-token-audience`, 600 seconds, and `--credential-file`. A cluster-owned Bot has no garam identity.
  - **A garam-owned Bot's mailbox.** Where `--mailbox-url`, `--mailbox-origin` and `--garam-root-configmap` are given, which the chart renders from `connector.garamMachine.url` and `.origin` and `connector.garamRoot` where garam and the connector are on, a garam-owned Bot alone also gets garam's root in its trust copy as `garam-root.crt`, and `--mailbox-url`, `--mailbox-origin`, `--garam-root-file` and `--bot-grn`, its GRN `grn:<org>/iam/bot/<declaration id>`, so its process long-polls its garam mailbox for its tasks (garam-bot ADR 0043 decision 7, garam ADR-0194). The root is read and watched through a cache of its own, holding that ConfigMap alone by a field selector on its name, so a replaced one reaches every copy. The chart refuses one of `connector.garamMachine`'s values without the other, and either with garam or the connector off.
  - **Its proxy.** Where `proxy.url` is set, the chart gives the manager `--bot-https-proxy` and `--bot-no-proxy`, and a garam-owned Bot given its mailbox gets them as `HTTPS_PROXY` and `NO_PROXY`: the connector's Service, `.svc`, `.cluster.local`, `localhost` and `127.0.0.1` are reached directly, and the runtime's clients of the connector use no proxy whatever the environment says. Without one, the Pod has neither, and dials garam directly.
  - **With no `--connector-url`**, which the chart gives only where the connector is on, a Bot gets no trust copy, no projected token and no connector flags, waits for no trust, and runs with no workspace.
- **A Bot's `Running` condition** is read off its Pod's runtime container: `True` while it is ready, and otherwise the reason its termination message names, `Starting`, `WaitingForConnectorTrust`, which every Bot waits in while the connector has published no trust, or `WaitingForGaramRoot`, which a garam-owned Bot given its mailbox waits in while the release holds no garam root. In a namespace not claimed for its release the manager writes nothing and logs once per Bot why.
- **Interface 1's kinds are garam-bot's interface.** Their Go types come from garam-bot's `api/declaration/v1alpha1`, and no second API group is scaffolded under `api/`, so `stack-kubebuilder.md` §1's single-group rule holds for this project's own APIs (garam-bot#1254).
- **The manager starts a reconciler for each kind the cluster serves**, found by discovery at start. The chart installs no `Agent` CRD, so the chart's manager runs no `Agent` reconciler. No mode flag is added. Where `--control-address` is set, a missing `Agent` kind fails the start.
- **The control-service path is not part of the chart.** `internal/desired`, the `Agent` refresher, the `Agent` kind and the control service stay as `agent.md` and `control.md` describe them, and do not run in a user's cluster.

### The connection

- **Every connection is opened by the connector, outbound, over HTTPS to one garam address.** garam never dials the cluster ([ADR 0087](adr/0087-connect-a-users-cluster-through-a-connector-it-installs-that-dials-garam-over-https-long-poll.md)).
- **Declarations arrive by long-poll, and reports leave by POST.** garam answers an open poll as soon as a change is written, and otherwise after at most 50 seconds.
- **The wire is the repository's `api/cluster-connection/v1alpha1/`** ([ADR 0092](adr/0092-specify-the-garam-connector-boundary-as-the-cluster-connection-interface-with-conformance-suites.md), ADR 0095): the normative specification, its OpenAPI schemas, and the conformance tests mapped to its requirements. garam implements the server, and an in-memory reference server beside the specification is the second implementation.

### Registration and identity

These follow [ADR 0088](adr/0088-register-a-connector-with-a-one-time-token-and-a-key-it-generates-in-the-cluster.md), and the rule over them is [ADR 0093](adr/0093-connect-every-cluster-aurora-included-by-a-registration-token-and-keys-it-generates-and-renews.md).

- **Every cluster connects this way, aurora included.** Its only credential at install is a registration token given once. Its key is generated in the process that holds it, kept there and in one Secret, and renewed without a person. No key is issued by garam or placed by hand.
- **A cluster is a garam resource, `grn:<org>/iam/cluster/<id>`, in one organization.** One chart release is one record. A second organization on the same cluster is a second release in another namespace.
- **Registration spends a one-time token.** The token lives one hour, is good for one attempt, and garam keeps only its hash. The connector generates an RSA 3072 key itself and sends the public JWK with the token.
- **The private key never leaves the cluster.** It exists in the connector's process and in one Secret in the release namespace, which `--identity-secret` names. That Secret also holds the record, the successor while a rotation is unresolved, and the SHA-256 of the registration token last presented, never the token. The connector never writes the key to a file, and signs each exchange with the key it holds through `internal/machinetoken`'s `Identity`. It writes the Secret only on the `resourceVersion` it last read, so it never writes over another process: on a conflict it stops, and its restart reads what the other wrote.
- **A registration token is presented once.** The connector records the attempt before it sends the token. After any answer but 429, or after none, it waits for a different token in its token file, across restarts too (REG-7).
- **The connector authenticates as garam's other machine callers do,** by `private_key_jwt` under `machine-token.v2`, through `internal/machinetoken`.
- **It rotates its own key every `--rotation-period`, 30 days unless set** ([ADR 0094](adr/0094-let-the-operator-set-how-long-the-connectors-key-signs-before-it-rotates.md)). A period of 0 or less is refused at start, and the chart value `connector.rotationPeriod` sets it. The rotation call carries an authorization the current key signed, naming the successor, and a proof the successor signed. The bearer token alone rotates nothing. garam's 24-hour rollover holds both keys live. A repeated rotation answers as the first did, and a connector with no answer tries its stored successor.
- **Revocation is garam's**: withdrawing a key or deleting the record refuses the next request. A lost key is registered again under the same record.
  - **Registering again is not built yet** ([ADR 0088](adr/0088-register-a-connector-with-a-one-time-token-and-a-key-it-generates-in-the-cluster.md) decision 7, #450). The connector reads its token file only while its Secret holds no record, so one whose key garam no longer accepts backs off its rotation without end and ignores a new token.
  - **Until it is, it is recovered by hand:** empty the identity Secret's data (`kubectl -n <release-namespace> patch secret <identity-secret> --type=json -p '[{"op":"remove","path":"/data"}]'`), place a new registration token in the token file, and restart the connector. The Secret itself stays, because the connector never creates one.

### Declarations and ownership

These follow [ADR 0089](adr/0089-sync-declarations-both-ways-and-give-each-object-one-owner.md).

- **Each object of a declared kind has one owner, `garam` or the cluster**, recorded by interface 1's managed-by marker. A garam-owned object's marker also names the cluster record that applied it.
- **Each watched namespace is claimed by one release**, through a Lease the chart renders naming the release. It never expires, and `helm uninstall` removes it. Helm refuses a second release's claim, and both programs act only under their own release's claim. The claim coordinates. The record's GRN in each object's marker is what isolates one record's objects from another's.
  - **Helm's refusal is its ownership check, which `helm install` runs and two paths skip.** Argo CD renders the chart with `helm template`, so a second release's Lease overwrites the first's. `helm install --take-ownership` adopts an existing Lease, with the same effect. Do not use it on a release that renders a claim.
  - **Both fail safe.** The first release then finds its claim naming another release, stops acting in that namespace, and reports it as `claimedElsewhere` (CLAIM-2).
  - `test/chart` installs a second release watching a namespace the first claimed into an envtest API server, and sees it refused with the first's claim unchanged (#1253).
- **garam-owned objects are applied by server-side apply, and drift in them is reverted:** a field a hand edit changed or added, in the spec, and a declared label or annotation it changed or removed.
- **Cluster-owned objects are reported to garam and are read-only there.**
- **A collision is refused, never overwritten.** A name held by a cluster-owned object, or by another record's object, makes the garam declaration a `conflict`.
- **Deletion is by explicit tombstone only.** garam records how far it has purged tombstones, and a cursor at or before that point reads the full set again. An object the full set no longer names is reported `orphaned` and left in place.
- **Losing garam changes nothing in the cluster.**
- **A declaration carries secrets only as `<secret-name>/<key>` references, and names no image.**

How the connector applies them (#441):

- **A pipeline.** The connector long-polls the declarations and holds the latest generation of each. A full set replaces what it holds only at its last page. A 410 reads the full set again. A pass reads the watched namespaces' claims and their objects, decides, and writes. It runs on every change of the declarations held, on every change of an object of a declared kind or of a claim, every 10 minutes, and again after a back-off when it failed. No pass runs before the first full set has been read.
- **What it checks before applying.** Interface 1's own parser, `manifest.Parse` from `github.com/garamsh/garam-bot/api/declaration/v1alpha1`, admits an object as the API server would under strict field validation: a declared kind, secrets by reference alone, and no field naming an image. The object also needs `metadata.namespace`. A declaration that fails any of these is `invalid`, and so is one the API server refuses.
- **What is applied.** The declaration's `apiVersion`, `kind`, namespace, name, labels, annotations and `spec`, with the ownership marker naming the record, the identifier and the generation. Nothing else of the object is applied.
- **How it writes.** It writes under one field manager, `garam-connector`, and only in the watched namespaces: a write to any other is refused before it is sent. An object is created where nothing holds its name. Every later write to the record's own object is three steps, each on the `resourceVersion` the step before answered, starting from the one the pass read, so an object changed since is decided again by the next pass rather than written over:
  1. Where a create left fields under the field manager's update entry, a JSON patch hands them to its apply entry, so a label a later generation drops is removed.
  2. A JSON patch replaces the spec whole with the declaration's. That removes a field a hand edit added to the spec, such as another egress host on a Bot, as well as one it changed.
  3. A server-side apply forces every field the declaration sets, the declared labels and annotations among them, back to the declaration's.

  A label or annotation a hand edit added and the declaration does not name is left in place: neither names a right a bot holds. A delete carries the object's uid and `resourceVersion` as preconditions.
- **Each declaration's outcome** is kept for the reports (#442):
  - `applied` where the object is the record's for that identifier, at the generation applied;
  - `conflict` where another object holds the name: a cluster-owned object, another record's, the record's own object for another declaration, or an object whose marker is malformed;
  - `invalid`, as above;
  - `namespaceNotWatched`.

  A declaration for a namespace claimed elsewhere has no outcome, because nothing is reported there. In report-only mode, a declaration is `applied` only where the cluster already holds that generation, and otherwise has no outcome.
- **An orphan** is one of the record's objects that no declaration held names, at the namespace and name its latest generation names.
- **Reports (#442)** are built from the same pass the outcomes come from: the objects as the pass read them, and the outcomes after its writes (REP-1). A write the pass made shows in the objects of the next report, which the write's own change wakes.
  - **What a report carries:** the pass's outcomes, the claimed and claimed-elsewhere namespaces, the mode, and the own objects of the claimed namespaces.
    - This record's garam-owned objects come with their declaration and whether they are orphaned.
    - Cluster-owned objects come with their spec.
    - Every object comes with its status.
  - **What it leaves out:** another record's object (CLAIM-3), and an object whose marker is malformed, which has neither owner (OWN-4).
  - **No Secret or ConfigMap can reach one:** the connector lists the four declared kinds and nothing else, so neither is ever read (REP-3).
  - **When it is sent:** once a pass finds the view changed, which a change of an object or a claim, or of the declarations held, wakes at once. An unchanged view is sent again every 5 minutes (REP-4).
  - **When garam does not take it:**
    - A 413 is logged, and the view is sent again once it changes; declarations go on being applied (REP-6). A view whose encoding is over 4 MiB is treated the same way without being uploaded.
    - A 429 waits its `Retry-After`, at most 60 s.
    - A 5xx or no answer is backed off from 1 s, doubling to 60 s (T-6). Any other refusal is backed off the same way and logged at error, since sending it again cannot help.
    - Nothing is sent before the wait ends, neither for a pass nor for the 5-minute timer. A pass that ends during the wait is held, and the latest view is sent when the wait ends.
- **A tombstone** deletes the record's objects for its identifier, in the claimed namespaces. A full set read after it drops it, so what it named and did not delete is reported `orphaned`. garam cannot yet tell that orphan from one it never asked to delete (#453).

### Bots' tokens

These follow [ADR 0097](adr/0097-give-a-garam-owned-bots-process-its-token-from-the-connector-over-tls-on-its-projected-serviceaccount-token.md) and garam-bot's ADR 0042. The protocol is garam-bot's interface 6, `api/bot-token/v1alpha1/`.

- **A garam-owned Bot's process obtains its token from the connector.** Every bot token is bound to a key the bot's process holds (garam ADR-0191). The process sends its proof, and the connector makes garam's exchange with it and answers the token. The connector never holds the key, signs no proof, writes no Secret, and keeps no token past its answer.
- **Who is served.** A Bot the connector applied itself, from a declaration it still holds, in a watched namespace. The object at the place the declaration names must be the one the connector made, by UID. The declarations domain answers it from its own writes, `Service.Bot`, and the object's ownership marker is never read for it, because any writer of the namespace can set one. Any other Bot is answered `unknown_bot` before garam is asked: a cluster-owned one, one with a forged marker, the orphan a move left, and an object made in place of the connector's.
- **The exchange.** garam's token endpoint, the `tokenUrl` the record holds, with `grant_type` token exchange, the cluster's token as `actor_token`, the bot's GRN `grn:<org>/iam/bot/<declaration id>` as `subject_token`, no `scope`, and the process's proof unchanged as `DPoP`.
  - It is sent over the client that verifies garam against `--garam-root-file`, without the cluster's bearer header.
  - The connector keeps the `DPoP-Nonce` garam last answered, and answers it to a process whose proof carries none.
  - garam's refusal of the bot, 400 `invalid_request` or `invalid_grant`, is `refused`. Its 429 is relayed with `Retry-After`. A refusal of the proof is relayed as garam gave it.
  - garam's 401 refuses the cluster's own token. The connector drops that token and exchanges once more under a fresh one, with the same proof. A second 401, any other refusal, a 5xx or no answer is `temporarily_unavailable`, 503, never `refused`.
- **The place's credential is the Bot's projected ServiceAccount token.**
  - **The ServiceAccount.** Its name is the Bot's, in the Bot's namespace, and it holds no rights. The manager creates it with the Bot's workload (#1254).
  - **The token.** Its audience is `--bot-token-audience`, `garam-connector`, and it lives 10 minutes. A token whose `exp` - `iat` is more than one hour is refused.
  - **The check.** It is verified against the keys and issuer the API server publishes at `/openid/v1/jwks` and `/.well-known/openid-configuration`, read through the default `system:service-account-issuer-discovery` binding. The keys are read again every 5 minutes, or sooner for a key not held. Credentials that arrive at once wait on one read.
  - **The Pod.** The token's `kubernetes.io` claim must name a Pod, by name and UID, in the Bot's namespace. That Pod must exist with that UID, run under the Bot's ServiceAccount, and be the Pod of the StatefulSet whose controller is the Bot object the connector made. Both are read from the API server, not a cache. The manager makes the Bot the StatefulSet's controller (#1254).
  - **Single use.** A token that obtained a garam token is refused afterwards, whatever key signs the proof. Requests carrying one token at once obtain at most one garam token.
  - **Across a restart.** Spent tokens are held in memory alone. A token issued before the connector started is refused, so one spent before a restart is not spent again. The kubelet replaces it within 8 minutes.
- **One replica.** The chart runs the connector with `replicas: 1` and the `Recreate` strategy, so two connectors never run at once with two spent sets (#1253).
- **A limit per Bot.** Each Bot has a bucket of 5 exchanges, refilled one every 30 seconds. A call past it is answered 429 `temporarily_unavailable` with `Retry-After`, to that Bot alone. Only a call that would reach garam takes from the bucket, and only from its own Bot's.
- **TLS the connector makes.**
  - **The certificate.** The connector serves on `--bot-token-address`, `:8443`, behind the Service `--bot-token-service`, with a P-256 key in its process alone and a certificate that key signs itself, valid 90 days.
  - **Publishing.** It publishes the certificate to `ca.crt` of the ConfigMap `--trust-configmap` before serving.
  - **Replacement.** At two thirds of the validity it publishes the next beside it, and serves the next 10 minutes later. A restart makes a new one.
  - **A failed publish** is made again after a back-off from 1 second to 1 minute, while the current certificate is served. It never stops the connector.
  - **Reaching the bots.** The manager copies the bundle into each watched namespace where it places a garam-owned Bot, and mounts it into the bot's Pod (#1254).
- **What is logged.** Each answer, with the Bot's namespace and name and the outcome. Never the credential, the proof or the token.
- **Who can reach a bot's token.** Whoever can change the Bot's StatefulSet, or exec into or read the files of its Pod, can use the bot's garam identity while that Pod runs. So the writers of workloads in a watched namespace are trusted with the bots placed there, and choosing `--watch-namespaces` grants that trust.

### The naru Machine provider

These follow garam-bot's ADR 0034 and naru ADR 0116. The provider is garam-bot's `pkg/naruworkspace`, interface 3's naru Machine implementation.

- **The connector runs it where `--naru-url` is set.** The chart sets it under `connector.naru`, off by default.
  - With it set, the connector refuses to start without the other options it requires: `--naru-root-file`, `--naru-registration-token-file`, `--naru-caller-secret`, `--naru-proxy-address`, `--naru-root-disk-bytes` and `--connector-deployment`.
  - Any `--naru-*` option without `--naru-url` is refused too. `--naru-proxy-root-file` is optional, and without it the system's roots verify naru-proxy.
- **It is established before the connector serves.**
  - The provider loads its caller pair from the caller Secret. With none, it registers once with the token file's token. A failure stops the start.
  - It then follows the caller's event log, and renews the certificate in its last 30 days, for as long as the connector runs.
- **The caller key and certificate stay in the caller Secret**, which the chart creates empty and the connector alone writes. They are never logged, reported or put in a workspace.
- **A refused renewal is a Warning Event on the connector's Deployment**, reason `NaruCallerCertificate`, and an error line in the log. The certificate then renews no more, and the operator places a new registration token before it expires. Interface 4's report carries no such warning yet (garam-bot#1248).
- **Its egress** is the ranges and ports `connector.naru.egress` names, for orch and naru-proxy.

### Bots' workspaces

These follow [ADR 0099](adr/0099-serve-every-bots-process-its-workspace-from-the-connector-in-one-dedicated-workspace-namespace-also-with-garam-off.md) and garam-bot's ADR 0043. The protocol is garam-bot's interface 7, `api/workspace-access/v1alpha1/`. Built in `internal/workspaceaccess` and `cmd/connector`, and off until `--workspace-namespace` is given, which the chart renders from `workspaces.namespace`. The manager gives every Bot its credential and trust (§The chart and its two programs).

- **The connector serves every Bot's process its workspace**, on interface 6's listener, Service and certificate.
  - **Who is served.** A Bot in a namespace claimed for its release, garam-owned or cluster-owned, resolved from its credential as interface 6 resolves it but on the audience `--workspace-audience`. Interface 6's "applied by the connector itself" is not required.
  - **Garam off.** It serves interface 7 also with garam off: `--garam-url`, `--garam-root-file` and `--registration-token-file` are given together or not at all, and with none it registers nothing, polls no declarations, sends no reports, and answers every interface 6 call 403 `unknown_bot` before reading its credential. The chart turns garam off where `connector.garamURL` and `connector.garamRoot.configMap` are both unset, and refuses one without the other.
- **The credential and the key.**
  - **The credential** is reused while it lives, and one issued before the connector started is refused.
  - **The instance** is the Pod's UID with its `runtime` container's `containerID` while that container runs. A Pod whose `runtime` container is not running is refused.
  - **The binding cache.** A credential's binding is kept 10 seconds at most. It is read again live before `invalid_key` is answered, and before any refusal where the cached read disagrees with the credential, as it does for a Pod made again under its name.
  - **The pin.** The first key whose DPoP proof verifies for an instance is pinned. Another key for that instance is answered 401 `invalid_key`. Pins are held in memory alone, and a gone Pod's pins, or a restarted container's, are dropped when it is read again every 2 seconds.
- **What a process may do:** acquire its own attachment's workspace, write up to 16 files at once under `/run/garam-bot/`, dial port 7700, and obtain the session's tokens. No exec, no file read, no delete.
- **The workspace namespace, `--workspace-namespace`,** holds every workspace. It is never a watched namespace or the release namespace.
  - **The chart renders it,** labelled for its release and `pod-security.kubernetes.io/enforce: restricted`, so Helm refuses a second release that names it.
  - **The default ServiceAccount.** The connector sets `automountServiceAccountToken: false` on its `default` ServiceAccount at start, before it serves any workspace. It waits while that ServiceAccount is not made yet, and through a conflicting update.
  - **The Pod provider**, moved to garam-bot's `pkg/podworkspace`, runs there with the rights garam-bot `interfaces.md` lists. Its `Capabilities.Egress` is `workspaces.pod.enforcesNetworkPolicy`, true by default, which needs a network plugin that enforces NetworkPolicy egress.
  - **The naru Machine provider** stays as it runs. A Machine's port is guarded by the adapter's token alone, since naru-proxy serves it to whoever reaches it. Its file write does not yet promise to follow no link, as interface 7's FILE-1 requires, so a link planted in a Machine can redirect a token file within that Machine alone, until naru#455 (ADR 0099's errata).
  - **Choosing a provider.** A WorkspaceClass's `spec.provider`, `pod` or `naru`, picks one.
- **Each workspace is named per Bot and per release, and isolated.**
  - **Naming.** Its name is `w<release>-<bot>`, hashes of the release and of the Bot's namespace, name, UID and attachment. It carries labels naming the release and the Bot, where its provider keeps labels: the Pod provider does, and the naru Machine provider does not yet.
  - **Ingress.** Its NetworkPolicy admits no ingress, since `Dial` is `portforward`.
  - **The API server, directly.** The Pod provider's `portforward` reaches the API server directly, through no proxy, whatever `rest.Config` or the environment names. The connector reaches its own cluster's API server in-cluster. A dial ends at its context's end or at 30 seconds, whichever is first.
  - **Egress** is per provider: `--pod-workspace-egress` and `--naru-workspace-egress`, as `<cidr>/<protocol>/<port>` rules that must include DNS, rendered from the chart's `workspaces.pod.egress` and `workspaces.naru.egress`, which it refuses without a rule to port 53. The gateway and tool proxy URLs are `--pod-model-gateway-url`, `--pod-tool-proxy-url`, `--naru-model-gateway-url` and `--naru-tool-proxy-url`.
  - **Deletion.** The sweep takes only this release's workspaces, by prefix and labels and a name its labels give, and only from a provider that keeps labels. It leaves those of a namespace no longer watched or no longer claimed. It runs every minute. It deletes one only after a live `GET` of its Bot answers NotFound or another UID, and 10 minutes have passed since it first found it so. A workspace whose labels it cannot read is never deleted.
  - **naru Machines are not swept yet.** The naru Machine provider keeps no labels until naru#453 adds caller-set Machine metadata labels, so until then a deleted Bot's naru Machines are deleted by hand, by the place's operator (ADR 0099's errata).
- **Images.** The workspace image is `--agent-image <harness>=<ref>` by digest, and the Pod provider's file helper is `--workspace-helper-image`. The chart renders them from `images.agents` and `images.workspaceHelper`, and refuses one not pinned by digest.
- **Session tokens.** Before G3 and G6 there is no production issuer, so the call answers 503 `waiting_for_gateway`. The stub issuer is compiled only under the build tag `e2e`, into the end-to-end suite's own connector image (garam-bot#1252).
- **Limits.**
  - **Per Bot.** Each Bot has a bucket of 20 calls that reach a provider or an issuer, refilled one every 3 seconds, answered 429 past it to that Bot alone. It is taken before the call's first provider call, so a call past it asks no provider. A read of the workspace takes nothing, and a renewal is two calls.
  - **Per instance.** Each instance has a bucket of 30 requests, refilled one a second, taken right after the credential verifies, before any Pod is read.
  - **Dials.** At most 4 are open per instance, at most `--max-dials` (256) in all, and one idle for 120 seconds is closed. An instance's dials close when its Pod or container ends, its Bot goes, or its workspace stops, each read again every 2 seconds. A dial waits 30 seconds at most for its provider to connect.
- **A restart mid-`Dial`** breaks the connection. The process dials again once its credential is replaced, within 8 minutes, and the adapter keeps its session and events.
- **The rechecks.** The idle close reads nothing, and runs apart from the reads. The dial and pin rechecks each end at their 2-second period. Each Bot, Pod and workspace is read once a pass, and a workspace read still running past its pass is not started again. Interface 7's clients have a budget of 50 requests a second, bursting to 100, and every Pod and ServiceAccount request ends at 10 seconds.
- **What is logged.** Each call, with the Bot, the call and the outcome. Never a credential, a proof, a token, a file's content or the bytes of a dial.

### Rights in the user's cluster

These follow [ADR 0091](adr/0091-hold-only-namespaced-rights-in-a-users-cluster-open-no-inbound-port-and-keep-secrets-in-it.md).

- **Rights:** Roles and RoleBindings in the release namespace and in each watched namespace, and no ClusterRole at runtime. Where the bots' workspaces are on, the connector's Role in the workspace namespace as well (ADR 0099). The chart, `deploy/charts/garam-connector/`, renders them (#1253).
  - **Neither program holds any right on Secrets in a watched namespace** (ADR 0097 decision 5, ADR 0098), so neither can read one.
  - **Each can read and write every object of the kinds its Role names there**: the manager every ConfigMap, ServiceAccount and StatefulSet, the connector every object of interface 1's kinds. So a watched namespace holds the bots' objects and nothing the installer would not hand these two programs.
  - **The caches are restricted to those namespaces.** The manager caches the claim alone among Leases, by a field selector on its name, and in a release namespace it does not watch, the trust ConfigMap alone the same way, since RBAC applies `resourceNames` to a `list` or `watch` that carries one. garam's root has a cache of its own, narrowed the same way.
- **The manager's Roles narrow what its kubebuilder markers generate** into `config/rbac/role.yaml` and its leader-election Role. In each watched namespace: `get`, `list` and `watch` on Bots and Pods, `update` on Bots' status, `get`, `list`, `watch`, `create` and `update` on StatefulSets, ConfigMaps and ServiceAccounts, and the claim read by name. In the release namespace: its leader-election Lease, events, the trust ConfigMap read by name, and, where garam's mailbox is on, garam's root ConfigMap read by name. `test/chart` fails a chart Role that grants the manager a right the markers do not.
- **The connector reads Pods and StatefulSets** in each watched namespace, `get` alone, to bind a credential to the Bot's Pod (ADR 0097).
- **The trust ConfigMap is the chart's to create, empty, and the connector's to write**, as the identity Secret is: `get` and `update` on that one ConfigMap by `resourceNames`, and no `create`, `list` or `watch`. Every write carries the `resourceVersion` read. The connector reads the API server's ServiceAccount issuer and keys through the default `system:service-account-issuer-discovery` binding, and holds no other right for it (ADR 0097).
- **The identity Secret is the chart's to create, empty, and the connector's to write.** The connector never creates a Secret, because RBAC cannot narrow `create` by name. Its Role grants `get` and `update` on that one Secret by `resourceNames`, and no `create`, `list` or `watch`. The chart never writes the Secret's data after creating it, so an upgrade keeps the key.
- **Where `connector.naru` is on, the naru caller Secret** is held the same way: `get` and `update` on it by `resourceNames`. The connector's Role also grants `create` on Events in the release namespace, for the provider's Warning Event. `test/chart` fails a render that grants either with `connector.naru` off.
- **The connector's Role in each watched namespace** grants `list` and `watch` on interface 1's four kinds, and `create`, `patch` and `delete` on them where garam is on, since it applies garam's declarations alone, `get`, `list` and `watch` on Leases with `resourceNames: [garam-connector-claim]`, and `get` on Pods and StatefulSets. Where the bots' workspaces are on, it also grants `get` on Bots, for the deletion sweep's live read.
  - Every other read goes through the cache, which lists and watches, so no other `get` is needed on the four kinds. Server-side apply and both JSON patches are `patch`.
  - The cache asks for the claim by a field selector on its name, and RBAC applies `resourceNames` to a `list` or `watch` that carries one, so no other Lease is readable.
  - It never writes a Lease (CLAIM-2).
  - `internal/declaration/kube`'s tests run every read and write as a ServiceAccount bound to exactly this Role, and `test/chart` fails a chart that renders any other.
- **CRDs and the workspace Namespace:** installed by Helm with the installer's rights, and the only cluster-scoped objects the chart renders (ADR 0099). The Namespace is rendered only where `workspaces.namespace` is set, so Helm refuses a second release naming it, and `helm uninstall` deletes it with every workspace in it.
- **No inbound path from outside the cluster:** no LoadBalancer or NodePort Service, no external IPs, no Ingress, no webhook. A NetworkPolicy admits no ingress to the manager, and reaches the API server alone. The connector's admits ingress to its bot token port alone, from the bots' Pods of the watched namespaces alone (ADR 0097). Its egress goes to the configured proxy, or else to garam on 443, plus DNS, plus the API server's range given as a value, plus `connector.naru.egress` where the naru Machine provider is on. With garam off it goes to nothing of garam's, and the chart refuses a proxy. Decided, to build (ADR 0099): plus `connector.issuers.egress` for the session tokens' issuers. `test/chart` fails a chart that renders otherwise.
- **PodSecurity `restricted`** admits both programs' Pods. `test/chart` creates each from its installed Deployment in a namespace enforcing it.
- **Metrics:** off by default. Turned on, the manager serves plain HTTP inside the cluster, admitted from the peers the installer names alone. The connector serves none.
- **Secrets:** no Secret or ConfigMap is ever reported, and no credential reaches a log, metric, event, status or report. A report does carry two free-text fields of a cluster-owned object as written, `spec.persona.instructions` and a status condition's `message` (REP-2, REP-9), so it holds no credential only while nobody types one into them.
- **Images:** pinned by digest in the chart. A user can set the connector to report only.

## What garam owes

These are obligations on `garamsh/garam`, for that repository to decide and build. The interface is the part both sides share. The rest is garam's alone.

| Obligation | From |
|---|---|
| Serve `api/cluster-connection/v1alpha1/` and pass its server suite in CI | ADR 0092 |
| A Cluster resource, `grn:<org>/iam/cluster/<id>`, with browser routes to create, read, rename, describe and delete it, and to revoke its keys | ADR 0088 |
| Mint a one-time registration token on creation and on re-registration, under a new garam ADR superseding garam ADR-0117 in part | ADR 0088 |
| Store what ADR 0088 decision 8 lists, and nothing it excludes | ADR 0088 |
| `iam:cluster.register`, `.read`, `.update`, `.reissue` and `.delete` | ADR 0088 |
| Write a bot or workspace declaration placed on a cluster and namespace, with an identifier, a generation and a tombstone on delete | ADR 0089 |
| Refuse a declaration that names an image, carries a secret value, or takes a name a cluster-owned object holds in the latest report | ADR 0089, ADR 0091 |
| Show a cluster's reported objects, read-only where cluster-owned, with each declaration's outcome and `lastContactAt` | ADR 0089 |
| Let a cluster's token mint the garam tokens of a garam-owned bot placed on that cluster by a live declaration, and no other bot's, and reach no other operation (AUTH-4, AUTH-5), each bound to the key of the proof the bot's process signed (garam ADR-0191) | ADR 0090, ADR 0088, ADR 0097 |
| Treat a report as untrusted input: refuse undeclared kinds, ignore what names another record's declarations, and render cluster-owned specs as text only (REP-7 to REP-9) | ADR 0089 |

## Rationale

- **Why a connector rather than garam reaching the cluster:** the decision of 2026-11-24 (`garamsh/naru#373` §3) and ADR 0087's precedents.
- **Why long-poll:** ADR 0087's comparison. gRPC streams and WebSocket tunnels need a sticky replica, and a tunnel opens a path into the cluster.
- **Why a key generated in the cluster:** ADR 0088. The precedents are the GitHub runner's local RSA key and Open Cluster Management's klusterlet.
- **Why every cluster, aurora included:** the owner's direction of 2026-11-24, recorded in ADR 0093. Its precedents are GitHub Actions runners and Actions Runner Controller, and the Rancher agent.
- **Why one owner per object:** the 11-21 decision's managed-by (ADR 0089). The precedent is argocd-agent's managed and autonomous modes, chosen here per object.
- **Why two programs:** ADR 0090. ADR 0079's seam keeps identity provisioning outside the manager, and it keeps the key and the right to build workloads in different Pods.
- **Why the connector serves the bots' workspaces:** ADR 0099 and garam-bot ADR 0043. A process linking a provider would spread `pods/exec` or the naru caller certificate to every Bot, and a dedicated namespace keeps the connector's exec right away from the bots' own Pods.
- **Why the bot's process calls the connector for its token:** ADR 0097. The token is bound to a key only the process holds, so the process has to sign the proof and the connector has to make the exchange. Meeting through the Kubernetes API instead needs a Role per Bot and a token at rest.
- **Why namespaced rights and no images in declarations:** ADR 0091. They bound what a compromised garam or a stolen cluster token can do in a user's cluster.
- **Why an interface with suites:** ADR 0092. It is the owner's interfaces-first principle and `convention-driven-project`'s interfaces.md.

## Open questions

- **How the manager and the control service reach garam without an issued key.** Asked in `configuration.md` §Open questions, which describes those two processes' keys.
- **A cluster-owned bot's garam identity.** Does it get a GRN and tokens, and on whose authority?
- **Adoption.** An explicit action that moves an object between `garam` and the cluster as owner.
- **Several bots in one pod.** The 11-21 decision's one pod per organization, with a process per bot, waits for a garam-bot runtime that runs several bots. This goal runs one Bot per StatefulSet.
- **Sealed static keys.** The owner's decision of 2026-11-21, item 5, deferred past the first goal on 2026-11-24.
- **Where users pull the chart and the images from.** Today's repositories are private ECR (`delivery.md`), which a user's cluster cannot pull from.
