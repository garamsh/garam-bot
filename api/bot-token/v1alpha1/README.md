# Bot token v1alpha1

The normative specification of garam-bot's interface 6, the bot token, at version `v1alpha1`: the protocol by which a bot's process obtains its garam token from its execution place's connector. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Transport
- The place's credential
- The proof
- The exchange
- The limit
- Renewal
- Secrets
- Versioning and compatibility
- Open questions

## Scope and implementations

- **The client is a bot's process, and the server is its place's connector** (ADR 0024). Every token garam issues to a bot is bound to a key the bot's process makes and holds (garam ADR-0191, `docs/architecture/bot-system.md` §Authentication). The connector holds the place's garam identity and makes the RFC 8693 exchange. The process holds the key and signs the exchange's DPoP proof. This protocol carries the proof from the process to the connector, and the token back.
- **The server never holds the client's key, and the client never holds the place's.** The proof passes through the server unchanged, and the place's own token never reaches the client.
- **What the protocol carries:** one call, which answers garam's bot token for the bot the place's credential names, bound to the key that signed the proof.

The package's constants are the schema's names: the path, the headers and the error codes.

| Implementation | Place | State |
|---|---|---|
| `reference` | none: it issues its own place credentials, holds its bots in memory and mints its own tokens | the reference implementation, built, passing the server suite |
| `operator/cmd/connector` | a user's cluster (`operator/docs/architecture/connector.md`) | first production implementation, built; passing the server suite is checked by `garam-bot#1252` |
| the daemon | a PC or a naru host (ADR 0021) | second production implementation; to build |

## Transport

- **T-1.** The server MUST serve HTTPS, TLS 1.3 at least, and the client MUST verify the server's certificate against the trust the place gives it, and nothing else. The client MUST read that trust again when it changes, because the server makes and replaces its own certificate.
- **T-2.** The one call is `POST /bot-token/v1alpha1/token`, `TokenPath`, with no body. Answers are JSON. Both sides MUST ignore a member they do not know.
- **T-3.** Every refusal is `ErrorBody`, `{"error": <code>, "error_description": <text>}`, with RFC 6749's member names, and its code is one of `Code`'s. A client acts on the HTTP status when it does not know the code. A failure of the server's own, a panic among them, is answered 503 `temporarily_unavailable`, never with a code outside `Code`'s.

| Status | Code | Meaning |
|---|---|---|
| 200 | — | `Token`: garam's token, bound to the proof's key |
| 400 | `use_dpop_nonce` | Sign the proof again with the nonce in `DPoP-Nonce`, for the URL in `htu` (NONCE-1) |
| 400 | `invalid_dpop_proof` | garam refused the proof |
| 401 | `invalid_credential` | The place's credential is missing, does not verify, has expired, names another audience, lives longer than an hour, is not bound to the bot's running workload, was issued before the server started, or was spent (CRED-2, CRED-3) |
| 403 | `unknown_bot` | The credential names no bot the place runs under a garam identity (BOT-1) |
| 403 | `refused` | garam refused the exchange for this bot (BOT-2) |
| 429 | `temporarily_unavailable` | The bot's limit, or garam's, is reached: try again after `Retry-After` (LIMIT-1, REN-3) |
| 503 | `temporarily_unavailable` | garam or the server cannot answer now: try again after `Retry-After` where it is given (BOT-2, REN-3) |

## The place's credential

- **CRED-1.** Every request MUST carry `Authorization: Bearer <credential>`, where the credential is one the place issues to the bot's process and that names the bot. An `Authorization` header without the `Bearer ` scheme carries no credential. The client MUST read it again for each request, because the place replaces it. At a cluster it is a projected ServiceAccount token (`operator/docs/architecture/connector.md`).
- **CRED-2.** The server MUST verify the credential before it reads anything else of the request, and answer one that is missing or does not verify, has expired or names another audience than the server's with 401 `invalid_credential`.
  - **Its life.** A credential whose life, `exp` - `iat`, is more than one hour, or that carries no `iat`, MUST be answered the same. The place issues credentials far shorter, so a long one was issued outside the place's course.
  - **Its workload.** Where the place's credential names the workload instance it was issued to, the server MUST answer it the same unless that instance exists as named, runs the bot, and runs under the bot's identity at the place. At a cluster the instance is a Pod, by name and UID, in the bot's namespace: the Pod of the StatefulSet the bot controls, under the bot's ServiceAccount.
- **CRED-3.** A credential that obtained a token is spent: the server MUST answer every later request carrying it with 401 `invalid_credential`, whatever key signed its proof. A credential is spent only by a 200, and of requests carrying the same credential at once, at most one is answered 200. So a credential read in transit or from a log after its use obtains nothing. A credential taken before its use obtains one token, bound to the taker's key, and its rightful process's next request is refused, which shows the theft.
  - **Across a restart.** A server that holds its spent credentials in memory MUST answer a credential issued before it started with 401 `invalid_credential`, because a credential spent before the restart is no longer held. The place MUST run one such server at a time, so no two hold different spent credentials.

## The proof

- **KEY-1.** The client MUST make an ES256 key when its process starts, and MUST NOT write the private key to a file, send it or give it to another process. A new process makes a new key, and so obtains a new token.
- **PROOF-1.** The request carries in `DPoP` one proof the client signed with that key, as garam's token endpoint requires: a compact JWS with `typ` `dpop+jwt`, `alg` `ES256` and `jwk` the public key, and claims `htm` `POST`, `htu` the URL the server answered in `htu`, `iat`, a fresh `jti`, and `nonce` the one the server answered in `DPoP-Nonce`. garam's `platform/api/machine.yaml` §exchangeClientAssertion, at garam `371af83ebcc3a3af033929578e0b12abf54ed872`, is authoritative for its form.
- **PROOF-2.** The server MUST pass the proof to garam unchanged, as the exchange's `DPoP` header, and MUST NOT sign one.
- **NONCE-1.** A request with no proof, or with a proof that carries no nonce, MUST be answered 400 `use_dpop_nonce` with `htu`, garam's token endpoint URL, in the body, and with garam's current nonce in `DPoP-Nonce` where the server holds one. The server MUST keep the nonce garam last answered and answer the first request from it, so a client usually needs one request before the one that obtains a token. A nonce garam no longer accepts is answered by garam, and the server relays that answer with `htu` added. The client signs once more with the nonce answered, and MUST NOT retry `use_dpop_nonce` more than twice in a row without a pause.

## The exchange

- **BOT-1.** The server MUST answer 403 `unknown_bot`, without asking garam, a credential that names no bot the place runs under a garam identity: a bot the place does not hold, one declared at the place rather than in garam, or one another place's record applied.
  - **What the server trusts.** It MUST resolve the bot, and its declaration, from what the place itself applied, and MUST NOT take them from a mark on the bot that another writer at the place can set. So a bot marked as garam's by hand, one a moved declaration left behind, one whose declaration was deleted, and one made in place of the bot the place applied are each unknown.
- **EXCH-1.** For a bot it runs under a garam identity, the server MUST make garam's bot token exchange (RFC 8693): its own access token as `actor_token`, the bot's GRN `grn:<org>/iam/bot/<declaration id>` as `subject_token`, no `scope` while garam defines none, and the client's proof as `DPoP`.
- **BOT-2.** garam's answers are mapped so:
  - garam's refusal of the bot, 400 `invalid_request` or `invalid_grant` (no live declaration places it on the place's record), MUST be answered 403 `refused`.
  - garam's 429 MUST be answered 429 with its `Retry-After`.
  - garam's `use_dpop_nonce` and `invalid_dpop_proof` MUST be relayed with garam's `DPoP-Nonce`.
  - garam's 401 refuses the server's own token. The server MUST obtain a fresh token of its own and make the exchange once more, with the same proof, which garam has not spent. A second 401 MUST be answered 503.
  - Any other answer of garam, a 5xx, or no answer MUST be answered 503 `temporarily_unavailable`, never `refused`: they are garam's or the server's to cure, not the bot's.
- **TOK-1.** The server MUST answer garam's token as `Token`, with garam's `access_token`, `token_type` (`DPoP`) and `expires_in`, and MUST NOT keep it once answered.

## The limit

- **LIMIT-1.** The server MUST limit, for each bot, the exchanges it makes with garam, and answer a request past the limit 429 `temporarily_unavailable` with `Retry-After`, without asking garam. A request answered without asking garam takes nothing from a limit, and no bot's requests take from another bot's. The limit is the server's to choose, and MUST leave a process room for its renewals, each of which makes up to three exchanges.

## Renewal

- **REN-1.** The client MUST obtain a new token before two thirds of the current one's `expires_in` have passed, and at once when garam refuses the current one with 401. It MUST keep using the current token while it lives until a new one is answered, and MUST NOT use it once `expires_in` has passed or garam refused it.
- **REN-2.** A client whose request failed MUST wait before the next: from 1 second, doubling to 60 seconds, reset by a 200. A 401 `invalid_credential` is waited out the same way, because the place replaces a spent credential with a fresh one in its own time.
- **REN-3.** A 429 or 503 carrying `Retry-After` MUST be waited at least that long, at most 60 seconds.

## Secrets

- **SEC-1.** Neither side may write a token, a proof or a credential to a log, a metric, an event, a status, a file or a refusal: each MUST NOT. The client holds its token in memory only, and the server holds none past its answer.

## Versioning and compatibility

- **A release of `v1alpha1` may add** a member of an answer, an error code, or a header the server reads where it is optional. A client ignores an answer member it does not know (T-2), and acts on the status of a code it does not know (T-3).
- **A new version is required** to remove or rename anything, change the path, make a request header required, or change what a status means or when a credential is spent. A new version is served at a new path, beside the earlier one while clients move.
- **VER-1.** Each release has a number, `SpecRelease` in this package, from `0.1.0`, and the `api/` module's tag moves with every release.
- **A deprecated element** is marked so here and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the suite: the cluster connector and the daemon.

## Open questions

- **A client suite.** The server suite holds the server's rules. The client's (KEY-1, REN-1 to REN-3) are held by the runtime's own tests, until a suite that runs a client against the reference server is built.
- **A credential at a PC or a naru host.** Where the daemon is the bot process's parent, what it issues as the place's credential is decided with the daemon (ADR 0021).
