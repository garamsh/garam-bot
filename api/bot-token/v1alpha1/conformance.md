# Bot token v1alpha1 conformance

The tests an implementation of `README.md`'s server must pass, each named and mapped to the requirements it holds. KEY-1, REN-1 to REN-3 are the client's, and no suite holds them yet (`README.md` §Open questions). PROOF-2, EXCH-1, BOT-2 and TOK-1 name what the server sends garam and answers from it, which only a server that reaches garam shows: the reference server mints its own tokens, and `operator/internal/bottoken`'s tests hold them for the cluster's connector. CRED-2's workload binding and BOT-1's resolution from what the place applied need a place that runs workloads and applies declarations; the fixture names neither, and `operator/internal/bottoken` and `operator/internal/declaration`'s tests hold them for the cluster's connector.

## The suite

The suite reaches a server only through the protocol and the fixture below, never through its internals. It is built under `conformance/server/` beside this file, as a Go test behind the build tag `conformance`.

- `CONFORMANCE_BASE_URL` is the server's base URL, `CONFORMANCE_TRUST_FILE` a PEM file of the certificates it is trusted by (T-1), and `CONFORMANCE_FIXTURE` the fixture command, split on white space.
- `go test -tags=conformance ./bot-token/v1alpha1/conformance/server/` runs it from `api/`. `make conformance-bot-token`, from the repository root, runs it against the reference server.

### The fixture

Each subcommand exits 0 and prints its answer as one JSON object, or exits non-zero and prints why.

| Subcommand | Does | Prints |
|---|---|---|
| `credential <namespace> <name> [--audience <audience>] [--expired] [--lifetime <duration>] [--before-start]` | Issues the place's credential naming the bot `<namespace>/<name>`, as the place issues it to that bot's process. `--audience` names another audience than the server's; `--expired` issues one already expired; `--lifetime` issues one whose `exp` - `iat` is that Go duration; `--before-start` issues one whose `iat` is before the server started, alive for 10 minutes from now. | `credential` |
| `bot <namespace> <name> --owner garam\|cluster` | Places the bot at the place, declared in garam (`garam`, and placed on the place's record by a live declaration) or declared at the place (`cluster`). | `namespace`, `name`, `owner` |
| `binding <access-token>` | Reads garam's record of a token the server answered. | `jkt`, the RFC 7638 thumbprint of the key it is bound to |

## Server suite

| Test | Holds |
|---|---|
| `server/transport/refuses-tls-below-1.3` | T-1 |
| `server/token/bound-to-the-proofs-key` | PROOF-1, NONCE-1, TOK-1 |
| `server/token/asks-for-a-nonce-with-htu` | NONCE-1, T-3 |
| `server/credential/refuses-missing-or-unverifiable` | CRED-1, CRED-2, CRED-3 across a restart |
| `server/credential/spent-whatever-key` | CRED-3 |
| `server/credential/one-token-at-once` | CRED-3 |
| `server/bot/refuses-a-bot-not-garams` | BOT-1 |
| `server/limit/each-bot-alone` | LIMIT-1 |
| `server/secrets/no-token-in-a-refusal` | SEC-1 |

Every test that refuses carries its control, the same call that is answered, in the same test.
