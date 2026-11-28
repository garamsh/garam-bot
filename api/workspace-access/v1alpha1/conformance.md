# Workspace access v1alpha1 conformance

The tests an implementation of `README.md`'s server must pass, each named and mapped to the requirements it holds. KEY-1, AUTH-2, WS-3's wait, TOK-2, DIAL-2, REN-1 and VER-2 are the client's, and no suite holds them yet: `internal/bot`'s tests are to hold them for `garam-bot run`. CRED-2's binding to a place's own workload, PIN-1's binding cache and BOT-1's resolution from what a place runs need a place that runs workloads; the fixture models them, and the connector's tests hold them for the cluster.

## The suite

The suite reaches a server only through the protocol and the fixture below, never through its internals. It is `conformance/server/` beside this file, a Go test behind the build tag `conformance`.

- `CONFORMANCE_BASE_URL` is the server's base URL, `CONFORMANCE_TRUST_FILE` a PEM file of the certificates it is trusted by (T-1), and `CONFORMANCE_FIXTURE` the fixture command, split on white space.
- `go test -tags=conformance ./workspace-access/v1alpha1/conformance/server/` runs it from `api/`. `make conformance-workspace-access`, a stage of `make ci`, runs it against the reference server.
- **The cluster's connector** is to run it with the end-to-end suite on kind (garam-bot#1252).
- **The cases run one after another**, each with bots, instances and processes of its own. The idle case runs last, because it moves the server's clock for idle dials.

### The fixture

Each subcommand exits 0 and prints its answer as one JSON object, or exits non-zero and prints why.

| Subcommand | Does | Prints |
|---|---|---|
| `bot <namespace> <name> [--attachment <a>]... [--unsupported <a>]... [--tool <t>]...` | Places a bot with the attachments the server serves, those it cannot serve, and the tools it may use. A bot of the same name is replaced. | `namespace`, `name`, `uid` |
| `remake <namespace> <name>` | Deletes the bot and makes it again under its name, with a new identity. | `uid` |
| `remove <namespace> <name>` | Deletes the bot. | — |
| `instance <namespace> <name>`, `instance --no-bot` | Starts a workload instance of the bot, or a workload that runs no bot. | `instance` |
| `restart <instance>` | Restarts the instance's container, so it is a new instance under the same credential's name (PIN-1). | `instance` |
| `end <instance>` | Ends the instance. | `instance` |
| `credential <instance> [--audience <audience>] [--expired] [--lifetime <duration>] [--before-start]` | Issues the place's credential naming the instance, as the place issues one. | `credential` |
| `workspace <uid> <attachment> --stopped\|--failed\|--pending\|--deleting\|--running` | Moves the bot's workspace behind the server. | — |
| `file <uid> <attachment> <path>` | Reads what the workspace holds at a path, so a write is checked without interface 7 reading. | `exists`, `content` in base64, `link` |
| `link <uid> <attachment> <path> <target>` | Plants a link at a path, as code in the workspace can. | `link` |
| `exists <uid> <attachment>` | Whether the place holds the bot's workspace. | `exists` |
| `issuer --on\|--off` | Gives the place an issuer of session tokens, or takes it away. | — |
| `max-dials <n>` | Sets the server's cap on open dials (DIAL-3). | — |
| `clock --advance <duration>` | Moves the server's clock for idle dials alone (DIAL-3). | — |

Each workspace's interface 2 port echoes what it is sent, so a dial's bytes are checked.

## Requirements and their tests

Each case is a subtest of `TestServer`.

| Requirement | Tests |
|---|---|
| T-1 | `T-1/the server serves TLS 1.3 and refuses an older handshake` |
| T-2 | `T-2/a request member the server does not know is refused` |
| T-3 | Every case: a refusal must be `ErrorBody` with a code of `Codes` |
| CRED-1, CRED-2 | `CRED-2/a missing, expired, foreign, long or pre-start credential is refused`, `CRED-2/a credential of an instance that ended is refused`, `CRED-2/a credential is reused while it lives` |
| PROOF-1 | `PROOF-1/a proof for another method, URL, origin or credential is refused`, `PROOF-1/a proof within 60 seconds of the clock is taken and one past it is refused`, `PROOF-1/a seen jti is refused`. The challenge is read in the first CRED-2 case |
| PIN-1 | `PIN-1/a second key for one instance is refused with invalid_key`, `PIN-1/a restarted instance pins its new key`, `PIN-1/a new instance of the bot pins its own key`. Dropping a gone instance's pin changes no answer, since a restart or a new instance is a new instance, so it is the connector's tests' |
| BOT-1 | `BOT-1/a workload that runs no bot is unknown`, `BOT-1/an attachment of another bot is not found`, `BOT-1/a bot made again under its name reaches a new workspace and not the old` |
| WS-1 | `WS-1/acquire is idempotent and answers the workspace's state`, `WS-1/acquire starts a stopped workspace and recovers a failed one with its data`, `WS-1/an attachment the server cannot serve is unsupported`, `WS-1/a workspace still deleting is answered deleting`, `WS-1/a workspace whose bot is gone is deleted, and one whose bot lives is not` |
| WS-2 | `WS-2/a read before acquire is not found`, `WS-2/a read takes nothing from the bot's limit` |
| WS-3 | `WS-3/a pending workspace is answered with Retry-After` |
| FILE-1 | `FILE-1/files under the token directory are written whole and in order`, `FILE-1/the token directory itself, a sibling, a dot-dot and an unclean path are refused and nothing is written`, `FILE-1/a link at the final component is replaced, not followed`, `FILE-1/content above 64 KiB is refused`, `FILE-1/a workspace not running is answered not_running`. `TestCheckPath_FILE1` holds the path rule |
| DIAL-1 | `DIAL-1/a dial reaches the adapter's port and passes bytes unchanged`, `DIAL-1/another upgrade token is refused`, `DIAL-1/a workspace not running is answered not_running`. Every dial offers `http/1.1` alone and reads the 101's headers |
| DIAL-3 | `DIAL-3/a fifth open dial of one instance is answered 429`, `DIAL-3/the server's cap is answered 503`, `DIAL-3/a dial idle for 120 seconds is closed, and one idle for 50 is not` |
| DIAL-4 | `DIAL-4/a dial closes when its instance ends`, `DIAL-4/a dial closes when its bot is gone`, `DIAL-4/a dial closes when its workspace stops` |
| TOK-1 | `TOK-1/session tokens name the model and every tool`, `TOK-1/a server with no issuer answers waiting_for_gateway` |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone`, `LIMIT-1/a call refused before a provider is asked takes nothing` |
| LIMIT-2 | `LIMIT-2/an instance past its request limit is answered 429, refused requests counted` |
| LIMIT-3 | DIAL-3's cases |
| SEC-1 | `SEC-1/a refusal carries no token and no credential`. What a server logs is the connector's tests' |
| REACH-1 | Read by review: no call reaches past `TokenDir` and the adapter's port |
| KEY-1, AUTH-2, TOK-2, DIAL-2, REN-1, VER-2 | the client's, for `internal/bot`'s tests |
| VER-1 | `SpecRelease` in this package, read by review: the release is a constant, not behaviour |
