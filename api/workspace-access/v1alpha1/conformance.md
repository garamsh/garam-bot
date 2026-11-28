# Workspace access v1alpha1 conformance

The tests an implementation of `README.md`'s server must pass, each named and mapped to the requirements it holds. KEY-1, AUTH-2, WS-3's wait, TOK-2, TRUST-2, DIAL-2, REN-1 and VER-2 are the client's, and no suite holds them: `internal/bot`'s tests hold them for `garam-bot run`, against this reference server where it reaches them. CRED-2's binding to a place's own workload, PIN-1's binding cache and BOT-1's resolution from what a place runs need a place that runs workloads; the fixture models them, and the connector's tests hold them for the cluster.

## The suite

The suite reaches a server only through the protocol and the fixture below, never through its internals. It is `conformance/server/` beside this file, a Go test behind the build tag `conformance`.

- `CONFORMANCE_BASE_URL` is the server's base URL, `CONFORMANCE_TRUST_FILE` a PEM file of the certificates it is trusted by (T-1), and `CONFORMANCE_FIXTURE` the fixture command, split on white space.
- `go test -tags=conformance ./workspace-access/v1alpha1/conformance/server/` runs it from `api/`. `make conformance-workspace-access`, a stage of `make ci`, runs it against the reference server.
- **The cluster's connector** is to run it with the end-to-end suite on kind (garam-bot#1252).
- **The cases run one after another**, each with bots, instances and processes of its own. The idle case runs last, because it moves the server's clock past every open dial's idle time.
- **The server's own choices** are read from the fixture, never assumed: its limits from `limits`, its dial cap put back from what `max-dials` replaced, and its deletion of a gone bot's workspaces by `remove` and `remake`, which return once it is done.

### The fixture

Each subcommand exits 0 and prints its answer as one JSON object, or exits non-zero and prints why.

- **What a fixture cannot model.** A fixture that cannot do a subcommand at its place prints `{"cannotModel": <why>}` and exits 0. The case that asked skips with that reason, and assumes nothing in its place.

| Subcommand | Does | Prints |
|---|---|---|
| `bot <namespace> <name> [--attachment <a>]... [--unsupported <a>]... [--tool <t>]...` | Places a bot with the attachments the server serves, those it cannot serve, and the tools it may use. A bot of the same name is replaced. | `namespace`, `name`, `uid` |
| `remake <namespace> <name>` | Deletes the bot and makes it again under its name, with a new identity. The earlier bot's instances may run on. It returns once the place has deleted the earlier bot's workspaces, past whatever grace the place gives. | `uid` |
| `remove <namespace> <name>` | Deletes the bot. It returns once the place has deleted the bot's workspaces, as `remake` does. | `{}` |
| `instance <namespace> <name>`, `instance --no-bot` | Starts a workload instance of the bot, or a workload that runs no bot. | `instance` |
| `restart <instance>` | Restarts the instance's container, so it is a new instance under the same credential's name (PIN-1). | `instance` |
| `end <instance>` | Ends the instance. | `instance` |
| `credential <instance> [--audience <audience>] [--expired] [--lifetime <duration>] [--before-start] [--no-iat]` | Issues the place's credential naming the instance, as the place issues one. `--expired` is issued after the server started and expired since; `--before-start` is issued before the server started and alive; `--no-iat` carries no `iat`. | `credential` |
| `workspace <uid> <attachment> --stopped\|--failed\|--pending\|--deleting\|--running` | Moves the bot's workspace behind the server. | `{}` |
| `seed <uid> <attachment> <path> <content>` | Writes a file on the workspace's data volume, under interface 3's `DataPath`, as the bot's work there does, so what a stop and a recovery keep is checked. | `{}` |
| `file <uid> <attachment> <path>` | Reads what the workspace holds at a path, so a write is checked without interface 7 reading. | `exists`, `content` in base64, `link` |
| `link <uid> <attachment> <path> <target>` | Plants a link at a path, as code in the workspace can. The path may be a directory of a later write's path. | `link` |
| `exists <uid> <attachment>` | Whether the place holds the bot's workspace. | `exists` |
| `issuer --on\|--off` | Gives the place an issuer of session tokens, or takes it away. | `{}` |
| `max-dials <n>` | Sets the server's cap on open dials (DIAL-3). | `previous`, the cap it replaced |
| `clock --advance <duration>` | Moves the server's clock for idle dials (DIAL-3) and for proofs' `iat` and `jti` (PROOF-1). A credential's times stay the place's. | `{}` |
| `limits` | The server's limits: each bot's and each instance's burst, and the seconds each takes to give one call back (LIMIT-1, LIMIT-2). | `botBurst`, `botEverySeconds`, `instanceBurst`, `instanceEverySeconds` |

Each workspace's interface 2 port echoes what it is sent, so a dial's bytes are checked.

### The connector's harness

`operator/test/workspaceaccess` serves the connector's `internal/workspaceaccess` over stand-ins for kube-apiserver's keys and Pods, the watched namespaces' Bots and claims, and `workspacefake` as the Pod provider, and is its fixture. `make -C operator conformance-workspace-access` runs the suite against it in the operator's `make ci`. It is built under the `e2e` tag for the test hooks `max-dials` and `limits` read, and no release carries it.

- **What it models.** It signs each credential with a key the service trusts as the API server's, so `credential`'s flags are its own. Its provider decorates `workspacefake` with the phases `workspace` sets, an incarnation still deleting, and links that a write follows none of. `clock --advance` moves the service's clock.
- **The sweep.** `remove` and `remake` run the connector's deletion sweep twice through a test hook, the second pass with the sweep's clock past its grace, and return once it has deleted the earlier Bot's workspaces. The sweep reads its Bots from the fixture's map, as a live `GET` of the API server would answer. `exists` reads the provider.
- **What it leaves to the cluster.** CRED-2's binding to a Pod the Bot's StatefulSet controls, and BOT-1's resolution from it, are read by `internal/workspaceaccess/kube`, which the harness stands in for. Its tests and the suite on kind hold them.

### A cluster's fixture

The connector's fixture on kind (garam-bot#1252) reaches what kube-apiserver and the connector cannot do on request through test hooks built under the `e2e` tag, as ADR 0099 builds the stub issuer. Each is expected, and where one is not built the subcommand answers `cannotModel`:

- `credential --before-start`, `--expired` and `--no-iat`: a TokenRequest neither backdates a token nor issues one with less than 10 minutes to live, so the fixture signs these with a key the connector's test build trusts beside the cluster's.
- `clock --advance`: a clock the connector's test build takes from the fixture.
- `max-dials`, `issuer` and `limits`: the connector's flags, read and set through its test build.
- `workspace --failed`, `--pending` and `--deleting`: a provider of the connector's test build that the fixture moves.
- `remove` and `remake`: the fixture deletes the Bot, then advances the sweep's clock past its grace and waits for the deletion.

## Requirements and their tests

Each case is a subtest of `TestServer`.

| Requirement | Tests |
|---|---|
| T-1 | `T-1/the server serves TLS 1.3 and refuses an older handshake` |
| T-2 | `T-2/a request member the server does not know is refused` |
| T-3 | Every case: a refusal, a dial's among them, must be `ErrorBody` with a code of `Codes`. A panic answered 503 is the reference's `TestRecovering_AnswersAPanicWith503`, and the connector's tests' |
| CRED-1, CRED-2 | `CRED-2/a missing, expired, foreign, long, pre-start or iat-less credential is refused`, `CRED-2/a credential of an instance that ended is refused`, `CRED-2/a credential is reused while it lives` |
| PROOF-1 | `PROOF-1/a proof that is missing, repeated, unverified, of another typ, or for another method, URL, origin or credential is refused`, `PROOF-1/a proof within 60 seconds of the clock is taken and one past it is refused`, `PROOF-1/a seen jti is refused`, `PROOF-1/a jti seen 90 seconds ago is refused while its proof is within the window`. Every case reads each 401's challenge, its code and `algs` |
| PIN-1 | `PIN-1/a second key for one instance is refused with invalid_key`, `PIN-1/a restarted instance pins its new key`, `PIN-1/a new instance of the bot pins its own key`. Dropping a gone instance's pin changes no answer, since a restart or a new instance is a new instance, so it is the connector's tests' |
| BOT-1 | `BOT-1/a workload that runs no bot is unknown`, `BOT-1/an attachment of another bot is not found`, `BOT-1/a bot made again under its name reaches a new workspace and not the old`, `BOT-1/an instance of the bot's earlier incarnation reaches nothing` |
| WS-1 | `WS-1/acquire is idempotent and answers the workspace's state`, `WS-1/acquire starts a stopped workspace and recovers a failed one with its data`, `WS-1/a token file is gone after a stop or a recovery, and is written again`, `WS-1/an attachment the server cannot serve is unsupported`, `WS-1/a workspace still deleting is answered deleting`, `WS-1/a workspace whose bot is gone is deleted, and one whose bot lives is not` |
| WS-2 | `WS-2/a read before acquire is not found`, `WS-2/a read takes nothing from the bot's limit` |
| WS-3 | `WS-3/a pending workspace is answered with Retry-After` |
| FILE-1 | `FILE-1/files under the token directory are written whole and in order`, `FILE-1/the token directory itself, a sibling, a dot-dot and an unclean path are refused and nothing is written`, `FILE-1/a link at the final component is replaced, not followed`, `FILE-1/a link at an inner component is not followed, and the files before it stay written`, `FILE-1/a write of 17 files or of none is refused and nothing is written`, `FILE-1/content above 64 KiB in all is refused`, `FILE-1/a workspace not running is answered not_running`. `TestCheckTokenPath_FILE1` holds the path rule. A failure of the provider's `WriteFile` partway through is the connector's tests' |
| DIAL-1 | `DIAL-1/a dial reaches the adapter's port and passes bytes unchanged`, `DIAL-1/another upgrade token is refused`, `DIAL-1/a workspace not running is answered not_running`. Every dial offers `http/1.1` alone and reads the 101's headers |
| DIAL-3 | `DIAL-3/a fifth open dial of one instance is answered 429`, `DIAL-3/the server's cap is answered 503`, `DIAL-3/a dial idle for 120 seconds is closed, and one idle for 50 is not`. The cases dial one at a time, so the bounds under concurrent dials are the connector's tests' |
| DIAL-4 | `DIAL-4/a dial closes when its instance ends`, `DIAL-4/a dial closes when its bot is gone`, `DIAL-4/a dial closes when its workspace stops` |
| TRUST-1 | `TRUST-1/a model's trust is PEM roots, as CheckTrust takes them`. A root answered beside another for an hour before the gateway serves it needs a gateway that rotates; the connector's tests hold it for the cluster |
| TOK-1 | `TOK-1/session tokens name the model and every tool`, `TOK-1/a server with no issuer answers waiting_for_gateway` |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone, for each call that reaches a provider or an issuer`, `LIMIT-1/a call refused before a provider or an issuer is asked takes nothing` |
| LIMIT-2 | `LIMIT-2/an instance past its request limit is answered 429, refused requests counted` |
| LIMIT-3 | DIAL-3's cases |
| SEC-1 | `SEC-1/a refusal carries no token and no credential`. What a server logs is the connector's tests' |
| REACH-1 | Read by review: no call reaches past `TokenDir` and the adapter's port |
| Interface 3's errors | The table of README §Answers and refusals mapping a provider's errors: the connector's tests', since the reference links no provider |
| KEY-1, AUTH-2, TOK-2, TRUST-2, DIAL-2, REN-1, VER-2 | the client's, `internal/bot`'s tests: TRUST-2 once `garam-bot run` reads `model.trust`, which it does not yet (garam-bot#1211), so it gives its harness no trust and TRUST-2 does not bind it; AUTH-2 in `TestWorkspace_WaitsAfterEach401…`, WS-3's wait in `TestWorkspace_ReadsAPendingWorkspaceNoSoonerThanItsRetryAfter` and `TestPendingWait_…`, TOK-2 in `TestWorkspace_AcquiresTheWorkspace…` and `TestRenewal_…`, WS-1's rewrite in `TestWorkspace_WritesItsTokensAgainAfterAnAcquireThatStartedTheWorkspace`, REN-1 in `TestBackoff_…`, VER-2 in `TestKnownPhase_…`. DIAL-2 in `TestDriver_DialsAgainAfterABrokenConnectionFromOneSecondDoublingTo60ResetByA101`, against `agentfake`. KEY-1 is read by review, one key made per process for both interfaces |
| VER-1 | `SpecRelease` in this package, read by review: the release is a constant, not behaviour |
