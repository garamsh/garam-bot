# ADR 0097: Give a garam-owned Bot's process its token from the connector, over TLS the connector makes, on its projected ServiceAccount token

> Status: accepted
> Date: 2026-11-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- ADR 0090 decision 7 had the connector mint a garam-owned bot's tokens and write them to the bot's credential Secret. ADR 0091 decisions 2 and 4 gave it the rights to create and write Secrets in the watched namespaces, and opened no inbound path.
- garam now binds every bot token to a key the bot's process makes and holds, by DPoP (garam ADR-0191 at garam `371af83ebcc3a3af033929578e0b12abf54ed872`). The exchange needs a proof that key signed. The connector does not hold the key and must not hold it, so it can neither sign the proof nor use a token it wrote for the bot.
- garam-bot's ADR 0042 makes the bot token interface 6, `api/bot-token/v1alpha1/`. The bot's process calls its connector with the place's credential and its proof, and the connector answers the token.
- garam-bot#1255 asked for this design before it was built. The PM approved it on 2026-11-28 with one change: no cert-manager prerequisite on a user's cluster.

## Decision

1. **The connector serves interface 6 to the bots' processes of the watched namespaces.** This supersedes ADR 0090 decision 7, and ADR 0090 decision 3's tokens mounted beside the Bot.
   - It exchanges for a Bot only where the Bot is garam-owned, applied by its own record and in a watched namespace (AUTH-5's side in the cluster). A cluster-owned Bot is refused before garam is asked.
   - It makes garam's exchange with its own token as `actor_token`, the bot's GRN `grn:<org>/iam/bot/<declaration id>` as `subject_token`, and the process's proof passed through unchanged.
   - It keeps the DPoP nonce garam last answered, and answers it to a process that has none.
   - It writes no Secret, and keeps no token past its answer.
2. **The place's credential is the Bot's projected ServiceAccount token.**
   - **The ServiceAccount.** Each garam-owned Bot runs under a ServiceAccount of the Bot's name in its namespace. It holds no rights, and the manager creates it with the Bot's workload.
   - **The token.** The kubelet projects its token with the audience `garam-connector`, `--bot-token-audience`, for 10 minutes.
   - **The check.** The connector verifies the token against the API server's ServiceAccount issuer and keys, read from `/.well-known/openid-configuration` and `/openid/v1/jwks`. The default `system:service-account-issuer-discovery` binding lets every ServiceAccount read both, so no TokenReview is made and no ClusterRole is granted. It checks the audience and the expiry, and maps `system:serviceaccount:<namespace>:<name>` to the Bot `<namespace>/<name>`.
   - **Single use.** A token that obtained a garam token is refused afterwards. The kubelet replaces it once 8 minutes have passed, and the process renews after 40, so the process is not delayed in the normal course.
3. **The connector makes its own serving certificate.**
   - **The certificate.** It is a P-256 key in the connector's process alone, with a certificate that key signs itself. It names the Service `--bot-token-service` in the release namespace, `garam-connector` by default, and is valid for 90 days.
   - **Publishing.** The connector publishes the certificate to the ConfigMap `--trust-configmap` in the release namespace, under `ca.crt`, before it serves. The chart creates that ConfigMap empty. The connector's Role grants `get` and `update` on it by `resourceNames`, and writes carry the `resourceVersion` read.
   - **Replacement.** At two thirds of the validity it publishes the next certificate beside the current one. It serves the next one 10 minutes later, and the one after drops the old. A restart makes a new certificate and publishes it alone.
   - **Reaching the bots.** The manager copies the bundle into a ConfigMap of each watched namespace where it places a garam-owned Bot, and mounts it into the bot's Pod. The runtime reads it again as it changes.
4. **The connector listens on one port, `--bot-token-address`, `:8443`, behind a ClusterIP Service.** This supersedes ADR 0091 decision 4's "admits no ingress" for the connector alone. Its NetworkPolicy admits ingress to that port from the watched namespaces, and from nowhere else. Nothing outside the cluster reaches it: no LoadBalancer, NodePort, Ingress or route.
5. **The connector holds no Secret rights in the watched namespaces.** This supersedes ADR 0091 decision 2's Secret verbs for the connector. Its rights there are the four kinds and the claim Lease. In the release namespace it holds its identity Secret and the trust ConfigMap, each by name.

## Consequences

- No bot token is at rest in the cluster. A Secret's reader learns no token, and a token read in transit is worthless without the key that only the bot's process holds.
- The connector's rights shrink: it can no longer read or write any Secret of a watched namespace.
- The manager gains three things to build with the Bot reconciler (#1254): a ServiceAccount per garam-owned Bot, the projected token volume, and the trust bundle copied and mounted. It needs `get` on the trust ConfigMap in the release namespace, and `create` and `update` on ConfigMaps and ServiceAccounts in the watched namespaces.
- The chart (#1253) renders the Service, the empty trust ConfigMap, the connector's Role on it, and the NetworkPolicy's ingress rule.
- A restart of the connector replaces the certificate. Processes fail TLS until the new bundle reaches them through the manager and the kubelet, and retry in that time. A process renews 20 minutes before its token expires, so a restart does not leave it without a token.
- Anyone who can run a Pod under a Bot's ServiceAccount, or read its projected token before the process uses it, obtains that bot's token. Writing Pods in a watched namespace already reaches every Secret there (ADR 0091 decision 2), so this adds no right.

## Signs the decision was wrong

- An API server whose issuer discovery the default binding does not open, so the connector cannot read the keys without a ClusterRole.
- Processes regularly fail TLS for minutes after a connector restart, because the bundle reaches them slower than the 20 minutes their tokens leave.
- A bot's process waits after a restart for a credential its predecessor spent, often enough to be seen. That would show that single use costs more than the theft it exposes.

## Rejected alternatives

- **The process writes its proof into a Kubernetes object, and the connector writes the token back.** It keeps ADR 0091's "no ingress". But it needs a Role for each Bot, which the manager would have to escalate its own rights to create. It also leaves a token at rest in the object.
- **A certificate from cert-manager.** It would be a prerequisite of every user's cluster, and the PM refused that.
- **TokenReview of the credential.** It is exact, revocation included, but needs the `system:auth-delegator` ClusterRole, which ADR 0091 decision 1 refuses.
- **No single use, accepting a stolen credential with another key.** A credential read from a log or in transit would obtain a token bound to the thief's key until it expired. Single use leaves only a credential taken before its use, and that theft shows when the rightful process is refused.
