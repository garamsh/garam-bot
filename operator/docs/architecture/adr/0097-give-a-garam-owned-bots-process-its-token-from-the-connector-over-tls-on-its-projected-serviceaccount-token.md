# ADR 0097: Give a garam-owned Bot's process its token from the connector, over TLS the connector makes, on its projected ServiceAccount token

> Status: accepted
> Date: 2026-11-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- ADR 0090 decision 7 had the connector mint a garam-owned bot's tokens and write them to the bot's credential Secret. ADR 0091 decisions 2 and 4 gave it the rights to create and write Secrets in the watched namespaces, and opened no inbound path.
- garam now binds every bot token to a key the bot's process makes and holds, by DPoP (garam ADR-0191 at garam `371af83ebcc3a3af033929578e0b12abf54ed872`). The exchange needs a proof that key signed. The connector does not hold the key and must not hold it, so it can neither sign the proof nor use a token it wrote for the bot.
- garam-bot's ADR 0042 makes the bot token interface 6, `api/bot-token/v1alpha1/`. The bot's process calls its connector with the place's credential and its proof, and the connector answers the token.
- garam-bot#1255 asked for this design before it was built. The PM approved it on 2026-11-28 with one change: no cert-manager prerequisite on a user's cluster. The PM's review of garam-bot#1268 then decided how the Bot is resolved, how a credential is bound, the one replica and the limit per Bot.

## Decision

1. **The connector serves interface 6 to the bots' processes of the watched namespaces.** This supersedes ADR 0090 decision 7, and ADR 0090 decision 3's tokens mounted beside the Bot.
   - **The Bot.** The connector exchanges for a Bot only where it applied that Bot itself, from a declaration it still holds, at the place that declaration's latest generation names. The object there must be the one the connector made, by UID. The subject is that declaration's GRN, `grn:<org>/iam/bot/<declaration id>`.
   - **Not the marker.** The object's ownership marker is never read for this. Any writer of the namespace can set a marker, so a forged marker, the orphan a move left, and an object made in place of the connector's are each refused before garam is asked. The declarations domain answers the Bot from its own writes (`internal/declaration`, `Service.Bot`).
   - **The exchange.** It makes garam's exchange with its own token as `actor_token`, the bot's GRN as `subject_token`, and the process's proof passed through unchanged.
   - **garam's 401.** It refuses the connector's own token, so the connector drops that token and exchanges once more under a fresh one, with the same proof, which garam spends only once the actor token authenticated. A second 401, and any refusal other than garam's refusal of the bot, are answered 503 to the process, never `refused`.
   - **The nonce.** It keeps the DPoP nonce garam last answered, and answers it to a process that has none.
   - **What it keeps.** It writes no Secret, and keeps no token past its answer.
2. **The place's credential is the Bot's projected ServiceAccount token, bound to the Bot's Pod.**
   - **The ServiceAccount.** Each garam-owned Bot runs under a ServiceAccount of the Bot's name in its namespace. It holds no rights, and the manager creates it with the Bot's workload.
   - **The token.** The kubelet projects its token with the audience `garam-connector`, `--bot-token-audience`, for 10 minutes. The token names the Pod it was projected into, by name and UID, in its `kubernetes.io` claim.
   - **The check.** The connector verifies the token against the API server's ServiceAccount issuer and keys, read from `/.well-known/openid-configuration` and `/openid/v1/jwks`. The default `system:service-account-issuer-discovery` binding lets every ServiceAccount read both, so no TokenReview is made and no ClusterRole is granted. It checks the audience and the expiry, refuses a token whose life, `exp` - `iat`, is more than one hour, and maps `system:serviceaccount:<namespace>:<name>` to the Bot `<namespace>/<name>`.
   - **The Pod.** The token must name a Pod of the Bot's namespace. That Pod must exist with that UID, run under the Bot's ServiceAccount, and be the Bot's workload: the Pod of the StatefulSet whose controller is the Bot object the connector made. The connector reads the Pod and the StatefulSet from the API server, not from a cache. A token of a deleted Pod, of another Pod under the same ServiceAccount, or of a Pod that runs a Bot made in place of the connector's is refused.
   - **Single use.** A token that obtained a garam token is refused afterwards. The kubelet replaces it once 8 minutes have passed, and the process renews after 40, so the process is not delayed in the normal course.
   - **Across a restart.** The connector holds spent tokens in memory alone. On start it refuses every token issued before it started, so a token spent before a restart cannot be spent again after it. No spent set is persisted.
3. **The connector makes its own serving certificate.**
   - **The certificate.** It is a P-256 key in the connector's process alone, with a certificate that key signs itself. It names the Service `--bot-token-service` in the release namespace, `garam-connector` by default, and is valid for 90 days.
   - **Publishing.** The connector publishes the certificate to the ConfigMap `--trust-configmap` in the release namespace, under `ca.crt`, before it serves. The chart creates that ConfigMap empty. The connector's Role grants `get` and `update` on it by `resourceNames`, and writes carry the `resourceVersion` read.
   - **Replacement.** At two thirds of the validity it publishes the next certificate beside the current one. It serves the next one 10 minutes later, and the one after drops the old. A restart makes a new certificate and publishes it alone.
   - **A failed publish.** It is made again after a back-off from 1 second, doubling to 1 minute, while the current certificate is served. It never stops the connector.
   - **Reaching the bots.** The manager copies the bundle into a ConfigMap of each watched namespace where it places a garam-owned Bot, and mounts it into the bot's Pod. The runtime reads it again as it changes.
4. **The connector listens on one port, `--bot-token-address`, `:8443`, behind a ClusterIP Service.** This supersedes ADR 0091 decision 4's "admits no ingress" for the connector alone. Its NetworkPolicy admits ingress to that port from the watched namespaces, and from nowhere else. Nothing outside the cluster reaches it: no LoadBalancer, NodePort, Ingress or route.
5. **The connector's rights in the watched namespaces are the four kinds, the claim Lease, and `get` on Pods and StatefulSets.** This supersedes ADR 0091 decision 2's Secret verbs for the connector: it holds no Secret right there. The Pod and StatefulSet reads are decision 2's binding. In the release namespace it holds its identity Secret and the trust ConfigMap, each by name.
6. **The connector runs one replica.** The chart's Deployment has `replicas: 1` and the `Recreate` strategy, so no two connectors run at once. Two would hold two spent sets, and a token spent at one would obtain a second token at the other.
7. **The connector limits each Bot's exchanges before garam is asked.** Each Bot has a token bucket of 5, refilled one every 30 seconds. A call past it is answered 429 `temporarily_unavailable` with `Retry-After`, to that Bot's process alone. Only a call that would reach garam takes from the bucket, and only from its own Bot's, so a Bot whose process sends failed proofs exhausts its own bucket and no other's.

## Consequences

- No bot token is at rest in the cluster. A Secret's reader learns no token, and a token read in transit is worthless without the key that only the bot's process holds.
- The connector can no longer read or write any Secret of a watched namespace. It gains `get` on Pods and StatefulSets there.
- The manager gains these to build with the Bot reconciler (#1254):
  - a ServiceAccount per garam-owned Bot;
  - the Bot as the controller of its StatefulSet, and the StatefulSet as the controller of its Pod;
  - the projected token volume, `expirationSeconds: 600`;
  - the trust bundle copied and mounted.
- The manager needs `get` on the trust ConfigMap in the release namespace, and `create` and `update` on ConfigMaps and ServiceAccounts in the watched namespaces.
- The chart (#1253) renders:
  - the Service;
  - the empty trust ConfigMap and the connector's Role on it;
  - the connector's `get` on Pods and StatefulSets in the watched namespaces;
  - the NetworkPolicy's ingress rule;
  - the one replica with the `Recreate` strategy.
- A restart of the connector replaces the certificate, and refuses every credential issued before it. A process fails TLS until the new bundle reaches it through the manager and the kubelet. It is refused until the kubelet replaces its credential, at most 8 minutes later. It retries in that time. A process renews 20 minutes before its token expires, so a restart does not leave it without a token.
- A process's garam token is no longer reachable by a Pod a person starts under the Bot's ServiceAccount. It is reachable by whoever can change the Bot's StatefulSet, or exec into or read the files of the Bot's Pod, in the Bot's namespace. That is a right those writers did not have before: the bot's garam identity, while the Pod they changed runs.
- So the watched namespaces' writers of workloads are trusted with the bots placed there. A person choosing `--watch-namespaces` grants that trust, and `connector.md` says so.

## Signs the decision was wrong

- An API server whose issuer discovery the default binding does not open, so the connector cannot read the keys without a ClusterRole.
- Processes regularly fail TLS for minutes after a connector restart, because the bundle reaches them slower than the 20 minutes their tokens leave.
- A bot's process waits after a restart, or after another process spent its credential, often enough to be seen. That would show that single use costs more than the theft it exposes.
- A process is answered 429 in the normal course of a renewal, which would show the bucket is too small.

## Rejected alternatives

- **The process writes its proof into a Kubernetes object, and the connector writes the token back.** It keeps ADR 0091's "no ingress". But it needs a Role for each Bot, which the manager would have to escalate its own rights to create. It also leaves a token at rest in the object.
- **A certificate from cert-manager.** It would be a prerequisite of every user's cluster, and the PM refused that.
- **TokenReview of the credential.** It is exact, revocation included, but needs the `system:auth-delegator` ClusterRole, which ADR 0091 decision 1 refuses.
- **No single use, accepting a stolen credential with another key.** A credential read from a log or in transit would obtain a token bound to the thief's key until it expired. Single use leaves only a credential taken before its use, and that theft shows when the rightful process is refused.
- **The Bot resolved from its object's ownership marker.** Any writer of the namespace can set the marker, so a Bot made by hand and marked as garam's would obtain the GRN of whatever declaration it named.
- **A spent set persisted across restarts.** It is a second store of state for the connector to keep consistent. Refusing credentials issued before the start closes the same replay with no state, at the cost of one credential refresh after a restart.
- **More than one replica.** It would need the spent set shared between replicas.
