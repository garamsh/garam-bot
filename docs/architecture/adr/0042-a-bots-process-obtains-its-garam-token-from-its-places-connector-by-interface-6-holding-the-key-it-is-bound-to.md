# ADR 0042: A bot's process obtains its garam token from its place's connector by interface 6, holding the key it is bound to

> Status: accepted
> Date: 2026-11-28

A record states one current decision and is kept current: update the body when the decision evolves; delete the record when the decision stops carrying meaning. Numbering stays continuous — a deletion or insertion renumbers the log, and references in other records are updated in the same PR. History lives in git, not in the log.

## Context

- Every token garam issues to a bot is bound by DPoP to a key the bot's process makes and holds (garam ADR-0191 at garam `371af83ebcc3a3af033929578e0b12abf54ed872`, `bot-system.md` §Authentication). The exchange is the connector's, because the connector holds the place's garam identity (ADR 0036, ADR 0037). The proof is the process's, because only the process holds the key.
- ADR 0033 had `garam-bot run` read its token from a file someone else wrote, and garam-bot-operator's ADR 0090 had the connector write it into the bot's Secret. A token in a file is worthless to a process that did not sign its proof, and a connector writing it would need the bot's key. So the proof has to travel from the process to the connector, and the token has to travel back.
- At a cluster the process and the connector are two Pods. They could meet through the Kubernetes API, with the process writing its proof into an object and the connector writing the token back. That needs a Role for each bot, which the manager would have to escalate its own rights to create, and it leaves a token at rest in the object. They can also meet directly, by the process calling the connector.
- The process and the connector are built and released separately, and the PC daemon will be a second connector (ADR 0039). `docs/convention/interfaces.md` makes such a boundary a versioned interface.
- The PM approved this design on #1255 (garam-bot#1255, 2026-11-28), with no cert-manager prerequisite on a user's cluster.

## Decision

1. **The bot token is interface 6, `api/bot-token/v1alpha1/`.** It has one call, which a bot's process makes to its place's connector. The call carries the place's credential naming the bot and a DPoP proof the process signed. It answers garam's token, bound to the proof's key.
2. **The key is the process's alone.** `garam-bot run` makes an ES256 key at start and keeps it in memory. It signs each proof itself, and never writes, sends or hands the private key to anyone. A new process makes a new key and obtains a new token.
3. **No bot token is at rest.** The connector passes the proof through unchanged and keeps no token past its answer. The process holds its token in memory alone, and no Secret or file holds one.
4. **A place credential obtains one token.** Once a token is answered for it, the server refuses the credential, whatever key signs the proof. A credential read after its use obtains nothing. One taken before its use obtains one token, bound to the taker's key, and the rightful process's next request is then refused, which shows the theft.
   - **Across a restart.** A server holding its spent credentials in memory refuses every credential issued before it started, and a place runs one such server at a time.
   - **Its bounds.** A credential living longer than an hour is refused. Where the place's credential names the workload instance it was issued to, the instance must exist and run the bot.
5. **The process renews.** It obtains a new token before two thirds of the current one's life have passed, and at once when garam refuses the one it holds with 401. It keeps the token it holds until a new one is answered, never past the token's expiry, and backs off from 1 to 60 seconds after a failure.
6. **`garam-bot run` takes `--connector-url`, `--connector-trust-file` and `--credential-file`** in place of `--token-file`. It reads the trust file again as it changes, and the credential file again for each request. Without `--connector-url`, the bot runs with no garam identity.
7. **The server resolves the bot from what its place applied**, never from a mark on the bot another writer can set. It limits each bot's exchanges with garam alone, so one bot's failed requests cost no other bot.
8. **At a cluster**, the connector serves interface 6 over TLS with a certificate it makes itself. The place's credential is the Bot's projected ServiceAccount token. garam-bot-operator's ADR 0097 decides both.

## Consequences

- A token cannot be used by anyone but the process that holds its key. Neither the connector, a Secret's reader nor the workspace holds that key.
- The process must reach its connector: at a cluster, a Service in the release namespace, which the connector's NetworkPolicy admits from the watched namespaces.
- A process that restarts in the same Pod may find the credential its predecessor spent. It waits until the place replaces the credential: at a cluster, the kubelet replaces a 10-minute token once 8 minutes have passed.
- A failed start no longer has a `TokenUnreadable` reason. A connector that cannot be reached, or that refuses, leaves the bot running, with the status file's `token.stale` set.
- No garam route accepts a bot token yet (`bot-system.md` §Authentication), so the process holds a token nothing presents. The runtime's `TokenSource.Refused` takes garam's 401 to the token held, and is called by no one until the first caller that presents the token, and so does a proof for each request.
- garam-bot#1207's cluster part is this decision. Its PC and naru-host part, and the daemon's credential, wait for the daemon (ADR 0039).

## Signs the decision was wrong

- A process waits minutes for a token after a container restart, because its place credential was spent before the kubelet replaced it. That would show that single use costs more than the theft it exposes.
- The connector becomes a point that bots' processes cannot reach at scale, or one whose loss stops every bot from renewing at once.
- A second place, the PC daemon, cannot serve interface 6 without changing it. That would show that its shape is the cluster's and not a place's.
