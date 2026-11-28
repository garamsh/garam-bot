# Workspace access v1alpha1

The normative specification of garam-bot's interface 7, workspace access, at version `v1alpha1`: the protocol by which a bot's process acquires its workspace from its execution place's connector, writes the workspace's token files, dials its agent's adapter, and obtains the session's model and tool tokens. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Transport
- Authentication
- Workspaces
- Files
- Dial
- Session tokens
- The limit
- Secrets
- Versioning and compatibility
- Open questions

## Scope and implementations

- **The client is a bot's process, and the server is its place's connector** (ADR 0043).
  - **The server's part.** It links interface 3's providers, holds their rights and credentials, and acts on a workspace only for the bot whose workspace it is.
  - **The client's part.** It holds no provider right. It is interface 2's caller: it writes the adapter's and the session's token files, and speaks to the adapter through this interface's `Dial`.
- **What the protocol carries:**
  - acquiring a workspace for one of the bot's declared attachments, and reading its state;
  - writing a file under the token directory;
  - one connection to interface 2's port;
  - the session's short-lived model and tool tokens.

  It carries nothing else of interface 3: no exec, no read of a file, no snapshot and no delete.
- **The server serves every bot the place runs,** whether garam declared it or the place did. Interface 7 needs no garam identity, so a bot declared at its place has a workspace too.

| Implementation | Place | State |
|---|---|---|
| `reference` | none: it issues its own place credentials, holds its bots and workspaces in memory over `workspacefake`, and issues stub session tokens | the reference implementation; to build |
| `operator/cmd/connector` | a user's cluster (`operator/docs/architecture/connector.md`, garam-bot-operator ADR 0099) | first production implementation; to build |
| the daemon | a PC or a naru host (ADR 0039) | second production implementation; to build |

## Transport

- **T-1.** The server MUST serve HTTPS, TLS 1.3 at least. The client MUST verify the server's certificate against the trust the place gives it, and nothing else, and MUST read that trust again when it changes. At a cluster, the listener and the trust are interface 6's.
- **T-2.** Every call is under `/workspace-access/v1alpha1/`. Answers are JSON, and both sides MUST ignore a member they do not know.
- **T-3.** Every refusal is `{"error": <code>, "error_description": <text>}`, with one of the specification's codes. A client acts on the HTTP status when it does not know the code. A failure of the server's own is answered 503 `temporarily_unavailable`.

| Call | Meaning |
|---|---|
| `PUT .../workspaces/{attachment}` | Acquire the workspace of the bot's attachment: create or start it, and answer its state (WS-1) |
| `GET .../workspaces/{attachment}` | The workspace's state (WS-2) |
| `PUT .../workspaces/{attachment}/files?path=<path>` | Write a file under the token directory (FILE-1) |
| `GET .../workspaces/{attachment}/dial` with `Upgrade` | One connection to interface 2's port (DIAL-1) |
| `POST .../workspaces/{attachment}/session-tokens` | The session's model and tool tokens (TOK-1) |

## Authentication

- **CRED-1.** Every request MUST carry `Authorization: Bearer <credential>`, a place credential naming the bot, of this interface's own audience. The client MUST read it again for each request. At a cluster it is a projected ServiceAccount token of the audience `garam-workspace`.
- **CRED-2.** The server MUST answer 401 `invalid_credential` to a credential that:
  - is missing or does not verify;
  - has expired or names another audience;
  - lives longer than an hour, or carries no `iat`;
  - was issued before the server started;
  - is not bound to a running workload instance of the bot, where the place's credential names one.

  Unlike interface 6's, the credential is not spent by use, and serves every request while it lives.
- **KEY-1.** The client MUST sign every request with the ES256 key its process made at start, interface 6's KEY-1 key, and MUST NOT write, send or hand over its private key.
- **PROOF-1.** Every request MUST carry in `DPoP` one proof (RFC 9449):
  - a compact JWS with `typ` `dpop+jwt`, `alg` `ES256` and `jwk` the public key;
  - the claims `htm` the method, `htu` the request's URL without its query, `iat`, a fresh `jti`, and `ath` the credential's hash.

  The server MUST refuse with 401 `invalid_dpop_proof` a proof that:
  - does not verify;
  - names another method, URL or credential;
  - whose `iat` is more than 60 seconds from the server's clock;
  - whose `jti` the server has seen within that window.
- **PIN-1.** The server MUST pin the first key whose proof verifies for a workload instance. It MUST answer every later request of that instance under another key with 401 `invalid_key`, the one answer for that case. A credential is projected into its instance alone, so the first key presented is the instance's process's. A credential read later, from transit or a log, obtains nothing without that key. A server that holds its pins in memory refuses credentials issued before it started (CRED-2), so no pin is lost to a restart. At a cluster the instance is the Pod's UID with its runtime container's ID, so a restarted container pins its new process's key.
- **BOT-1.** The server MUST resolve the bot, and its attachments, from the credential and what the place runs, never from the request. An attachment the bot does not declare MUST be answered 404 `not_found`, the same answer as an attachment of another bot.

## Workspaces

- **WS-1.** `PUT` acquires the workspace of the attachment.
  - **Idempotence.** It is idempotent: it creates the workspace where none exists, starts it where it is stopped, and otherwise changes nothing.
  - **Naming.** The server names the workspace from the bot's identity and the attachment, so no bot reaches another's.
  - **The spec.** It builds the workspace's interface 3 spec from the attachment's WorkspaceClass and the place's configuration: the image for the bot's harness, interface 2's `Port`, and egress to the place's model gateway and tool proxy.
  - **The answer:**
    - `phase`: interface 3's phases;
    - `reason`;
    - `model.baseUrl`: the place's model gateway;
    - `tools`: the name and MCP URL of each tool the bot may use, at the place's tool proxy.
  - **What it refuses.** An attachment the server cannot serve is answered 422 `unsupported` with its reason: a provider the place does not run, a harness with no image, or a form of attachment this release does not serve.
- **WS-2.** `GET` answers the same state and changes nothing.
- **VER-2.** A client MUST read a phase it does not know as `unknown`.
- **What the server deletes.** It deletes a workspace once no bot it serves declares its attachment. The client deletes nothing.

## Files

- **FILE-1.** `PUT .../files?path=` replaces the file at `path` with the body, whole, through interface 3's `WriteFile`.
  - **The path.** It MUST be absolute and clean, and under `/run/garam-bot/`, interface 2's token directory. Any other path MUST be answered 400 `invalid`.
  - **The size.** A body above 64 KiB MUST be answered 413 `invalid`.
  - **The answer.** It is 204 once the file is written. A workspace that is not running is answered 409 `not_running`.
- There is no read. The client knows what it wrote.

## Dial

- **DIAL-1.** `GET .../dial` with `Connection: Upgrade` and `Upgrade: workspace-access-dial/v1alpha1` answers 101 Switching Protocols once the server holds a connection to interface 2's `Port`, 7700, in the workspace. From then on the server MUST pass bytes both ways unchanged until either side closes, and MUST NOT read or log them.
  - **No other port.** The request names none.
  - **A workspace that is not running** is answered 409 `not_running`.
- **Authentication inside.** The connection authenticates nothing. The adapter's own token, interface 2's AUTH-1, is what the adapter takes.
- **DIAL-2.** A connection broken by the server, a restart among them, MUST be dialled again by the client after a back-off from 1 second, doubling to 60, reset by a 101. The adapter keeps its session and events, so the client reads on from the last event it read.

## Session tokens

- **TOK-1.** `POST .../session-tokens` answers a fresh model token and one fresh token per tool of WS-1's answer.
  - **The answer.** Each has its `expiresIn`. Each lives at most an hour, and grants nothing outside its own service and this bot's workspace.
  - **The issuers.** The server obtains them from the place's model gateway (interface 5) and tool proxy for the bot. It MUST NOT keep them past its answer.
  - **No issuer.** A server with no issuer configured MUST answer 503 `waiting_for_gateway`.
- **TOK-2.** The client MUST write the tokens into the workspace's token files with FILE-1, and MUST obtain and write new ones before two thirds of the shortest `expiresIn` have passed. So none in the workspace is more than an hour old (interface 2's CRED-1).

## The limit

- **LIMIT-1.** The server MUST limit, for each bot, the calls that reach a provider or an issuer. It MUST answer a call past the limit 429 `temporarily_unavailable` with `Retry-After`, to that bot alone.
  - **What takes from it.** A call refused before a provider is asked takes nothing, and no bot's calls take from another bot's.
  - **Its size.** The limit is the server's to choose, and MUST leave room for a client's course: an acquire, three file writes and a dial each renewal.
- **REN-1.** A 429 or 503 carrying `Retry-After` MUST be waited at least that long, at most 60 seconds.

## Secrets

- **SEC-1.** Neither side may write a credential, a proof, a token, a file's content or the bytes of a dial to a log, a metric, an event, a status or a refusal: each MUST NOT. The server logs a call with the bot, the call and its outcome, and nothing more.

## Versioning and compatibility

- **A release of `v1alpha1` may add:**
  - a call;
  - a member of an answer;
  - an error code;
  - a form of attachment WS-1 serves;
  - a phase.

  A client ignores an answer member it does not know (T-2), and reads a phase it does not know as `unknown` (VER-2).
- **A new version is required** to:
  - remove or rename anything, or change a path;
  - widen FILE-1's directory or DIAL-1's port;
  - make a request header required;
  - change what a status means.

  A new version is served at a new path, beside the earlier one while clients move.
- **VER-1.** Each release has a number, `SpecRelease` in this package, from `0.1.0`, and the `api/` module's tag moves with every release.
- **A deprecated element** is marked so here and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the suite: the cluster connector and the daemon.

## Open questions

- **The rest of an attachment.** This release serves an attachment by `template` with the `Bot` lifetime and `Exclusive` access. A `ref` to a Workspace object, `SharedRead` access and the `Session` lifetime are refused (WS-1).
- **A bot's own egress.** Its `permissions.egress` names hosts, which the server does not resolve into interface 3's prefixes in this release.
- **The issuers' wire.** How the server obtains a token from the model gateway and the tool proxy is interface 5's and G6's to decide.
- **A credential at a PC and a naru host** is decided with the daemon, as interface 6's is (ADR 0039).
- **Several workspaces for one bot.** A client drives its first attachment. Whether interface 2 reaches more than one is not decided.
