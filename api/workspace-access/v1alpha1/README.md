# Workspace access v1alpha1

The normative specification of garam-bot's interface 7, workspace access, at version `v1alpha1`: the protocol by which a bot's process acquires its workspace from its execution place's connector, writes the workspace's token files, dials its agent's adapter, and obtains the session's model and tool tokens. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Transport
- Answers and refusals
- Authentication
- Workspaces
- Files
- Dial
- Session tokens
- Limits
- Secrets
- Versioning and compatibility
- Open questions

## Scope and implementations

- **The client is a bot's process, and the server is its place's connector** (ADR 0043).
  - **The server's part.** It links interface 3's providers, holds their rights and credentials, and acts on a workspace only for the bot whose workspace it is.
  - **The client's part.** It holds no provider right. It is interface 2's caller: it writes the adapter's and the session's token files, and speaks to the adapter through this interface's dial.
- **What the protocol carries:**
  - acquiring a workspace for one of the bot's declared attachments, and reading its state;
  - writing files under the token directory;
  - connections to interface 2's port;
  - the session's short-lived model and tool tokens.

  It carries nothing else of interface 3: no exec, no read of a file, no snapshot and no delete. REACH-1 keeps it so.
- **The server serves every bot the place runs,** whether garam declared it or the place did. Interface 7 needs no garam identity, so a bot declared at its place has a workspace too.
- **The package is the schema:** its constants name `SpecRelease`, the paths, the token directory, the dial's upgrade token, the error codes and the bounds (`doc.go`, `access.go`), its types are the bodies, and `CheckTokenPath` is FILE-1's path rule.

| Implementation | Place | State |
|---|---|---|
| `reference` | none: it issues its own place credentials, models workload instances and their restarts, holds its bots and workspaces in memory, echoes on each workspace's interface 2 port, and issues stub session tokens | the reference implementation, built, passing the server suite |
| `operator/cmd/connector` | a user's cluster (`operator/docs/architecture/connector.md`, garam-bot-operator ADR 0099) | first production implementation; to build |
| the daemon | a PC or a naru host (ADR 0039) | second production implementation; to build |

## Transport

- **T-1.** The server MUST serve HTTPS, TLS 1.3 at least. The client MUST verify the server's certificate against the trust the place gives it, and nothing else, and MUST read that trust again when it changes. At a cluster, the listener and the trust are interface 6's.
- **T-2.** Every call is under `PathPrefix`, `/workspace-access/v1alpha1/`.
  - **Bodies** are JSON with the members named below.
  - **Unknown members.** Both sides MUST ignore a member they do not know in an answer. The server MUST refuse a request body naming a member it does not know with 400 `invalid`, so a newer client learns its member was not taken.
- **T-3.** Every refusal is `ErrorBody`, `{"error": <code>, "error_description": <text>}`, with one of `Code`'s codes. A client acts on the HTTP status when it does not know the code. A failure of the server's own, a panic among them, is answered 503 `temporarily_unavailable`, never with a code outside `Code`'s.

| Call | Meaning |
|---|---|
| `PUT .../workspaces/{attachment}` | Acquire the workspace of the bot's attachment: create, start or recover it, and answer its state (WS-1) |
| `GET .../workspaces/{attachment}` | The workspace's state (WS-2) |
| `PUT .../workspaces/{attachment}/files` | Write one or more files under the token directory (FILE-1) |
| `GET .../workspaces/{attachment}/dial` with `Upgrade` | One connection to interface 2's port (DIAL-1) |
| `POST .../workspaces/{attachment}/session-tokens` | The session's model and tool tokens (TOK-1) |

## Answers and refusals

| Status | Code | Meaning |
|---|---|---|
| 200 | — | `Workspace` (WS-1, WS-2) or `SessionTokens` (TOK-1) |
| 204 | — | The files are written (FILE-1) |
| 101 | — | The dial is open (DIAL-1) |
| 400 | `invalid` | A body, member, path or header the specification refuses (T-2, FILE-1, DIAL-1) |
| 401 | `invalid_credential` | The place's credential is missing, does not verify, has expired, names another audience, lives longer than an hour, was issued before the server started, or is not bound to a running instance of the bot (CRED-2) |
| 401 | `invalid_dpop_proof` | The proof is missing, does not verify, names another method, URL or credential, is outside the clock window, or replays a `jti` (PROOF-1) |
| 401 | `invalid_key` | The proof's key is not the key pinned to the instance (PIN-1) |
| 403 | `unknown_bot` | The credential names no bot the place runs: a workload that is not a bot's, or a bot in a namespace the place does not serve (BOT-1) |
| 404 | `not_found` | An attachment the bot does not declare, or the bot's workspace for it does not exist yet (BOT-1, WS-2) |
| 409 | `not_running` | The workspace is not running (FILE-1, DIAL-1) |
| 409 | `deleting` | The workspace's earlier incarnation is still being deleted; acquire again after `Retry-After` (WS-1) |
| 413 | `invalid` | The files' content is above 64 KiB (FILE-1) |
| 422 | `unsupported` | The server cannot serve the attachment as declared: its form, its class or its harness (WS-1) |
| 429 | `temporarily_unavailable` | A limit of the bot or of its instance is reached: try again after `Retry-After` (LIMIT-1 to LIMIT-3) |
| 503 | `waiting_for_gateway` | The place has no issuer of session tokens (TOK-1) |
| 503 | `temporarily_unavailable` | The server, a provider or an issuer cannot answer now: try again after `Retry-After` where it is given |

Interface 3's errors reach the client so:

| Provider error | Answer |
|---|---|
| `ErrInvalid` from the spec the server built, the class's resources among them | 422 `unsupported`, with the reason |
| `ErrUnsupported` (NET-1, EXEC-2, SNAP-3) | 422 `unsupported`, with the reason |
| `ErrConflict` | 503 `temporarily_unavailable`: the server's names are its own, so a conflict is the server's to cure |
| `ErrDeleting` | 409 `deleting` with `Retry-After` |
| `ErrNotRunning` | 409 `not_running` |
| `ErrNotFound` | 404 `not_found` on a read, acquire creates |
| `ErrFileNotFound`, `ErrSnapshotNotFound` | not reachable: interface 7 reads no file and restores no snapshot |
| any other failure | 503 `temporarily_unavailable` |

## Authentication

- **CRED-1.** Every request MUST carry `Authorization: Bearer <credential>`, a place credential naming the bot, of this interface's own audience. The client MUST read it again for each request. At a cluster it is a projected ServiceAccount token of the audience `garam-workspace`.
- **CRED-2.** The server MUST answer 401 `invalid_credential` to a credential that:
  - is missing or does not verify;
  - has expired or names another audience;
  - lives longer than an hour, or carries no `iat`;
  - was issued before the server started;
  - is not bound to a running workload instance of the bot, where the place's credential names one.

  Unlike interface 6's, the credential is not spent by use, and serves every request while it lives.
  - **The instance.** At a cluster it is the Pod the credential names, by name and UID, with its `runtime` container's `containerID`, read from `status.containerStatuses` while that container's state is `running`. A Pod whose `runtime` container is not running is no running instance.
  - **Caching the binding.** The server MAY keep a credential's binding to its instance for at most 10 seconds.
- **KEY-1.** The client MUST sign every request with the ES256 key its process made at start, interface 6's KEY-1 key, and MUST NOT write, send or hand over its private key.
- **PROOF-1.** Every request MUST carry in `DPoP` one proof (RFC 9449): a compact JWS with `typ` `dpop+jwt`, `alg` `ES256` and `jwk` the public key, and the claims below.
  - **The claims.** `htm` the method, `htu` the request's URL without its query, `iat`, a fresh `jti`, and `ath` the base64url SHA-256 of the credential.
  - **What the server refuses.** It MUST answer 401 `invalid_dpop_proof` to a proof that:
    - does not verify;
    - names another method or credential;
    - names a `htu` whose scheme, host and port are not one of the server's configured origins, or whose path is not the request's;
    - carries an `iat` more than 60 seconds from the server's clock;
    - carries a `jti` the server has seen in the last 120 seconds.
  - **The challenge.** Every 401 carries `WWW-Authenticate: DPoP error="<code>", algs="ES256"`.
- **PIN-1.** The server MUST pin the first key whose proof verifies for a workload instance, and MUST answer every later request of that instance under another key with 401 `invalid_key`, the one answer for that case.
  - **Why the first key is the right one.** A credential is projected into its instance alone, so the first key presented is the instance's process's. A credential read later, from transit or a log, obtains nothing without that key.
  - **A restarted container** is a new instance, so its new process pins its own key. Before it answers `invalid_key`, the server MUST read the instance again from its source, not from a cache, so a restart it has not yet seen is not refused.
  - **One server.** The place MUST run one such server at a time, so no two hold different pins.
  - **Pins are lost to a restart of the server.** Because CRED-2 refuses credentials issued before it started, a credential leaked before the restart cannot pin a foreign key after it.
  - **Dropping pins.** The server MUST drop the pin of an instance that no longer exists.
- **BOT-1.** The server MUST resolve the bot, and its attachments, from the credential and what the place runs, never from the request.
  - **A workload that is not a bot's,** or a bot in a namespace the place does not serve, is answered 403 `unknown_bot`.
  - **An attachment the bot does not declare** is answered 404 `not_found`, the same answer as an attachment of another bot.
- **AUTH-2.** A client that is answered 401, whatever its code, MUST wait before its next request: from 1 second, doubling to 60 seconds, reset by an answer that is not 401. The place replaces a credential in its own time, and a server that restarted refuses the earlier one until then.

## Workspaces

- **WS-1.** `PUT` acquires the workspace of the attachment, with no body.
  - **What it does.**
    - It creates the workspace where none exists, and starts it where it is stopped.
    - Where it has failed, it recreates the workspace's workload, keeping its data volume as a stop and a start do. An interface 3 implementation whose failed workspace has lost its volume answers `failed` with that reason.
    - Otherwise it changes nothing.
  - **Naming.** The server names the workspace from the bot's identity and the attachment, so no bot reaches another's, and a bot made again under the same name, with a new identity, gets a new workspace.
  - **The spec.** It builds the workspace's interface 3 spec from the attachment's WorkspaceClass and the place's configuration: the image for the bot's harness, interface 2's `Port`, and the egress the place gives that provider's workspaces.
  - **What it refuses.** An attachment the server cannot serve is answered 422 `unsupported` with its reason: a provider the place does not run, a harness with no image, a class interface 3 refuses, or a form of attachment this release does not serve.
- **WS-2.** `GET` answers the same `Workspace` and changes nothing. It answers 404 `not_found` before the first acquire.
- **The answer.** `Workspace` is:
  - `attachment`: the attachment's name;
  - `phase`: one of interface 3's phases;
  - `reason`: why it is pending, failed or unknown, and empty otherwise;
  - `model`: `{"baseUrl": <url>}`, the model gateway this workspace reaches;
  - `tools`: an array of `{"name": <tool>, "url": <MCP URL>}`, one per tool the bot may use, at the tool proxy this workspace reaches.
- **WS-3.** While the phase is `pending`, the answer MUST carry `Retry-After`, and the client MUST NOT read again sooner. A client that is given none backs off from 1 second, doubling to 60.
- **VER-2.** A client MUST read a phase it does not know as `unknown`.
- **What the server deletes.** It deletes a workspace once the bot that owned it is gone, as the place decides it safely (at a cluster, garam-bot-operator ADR 0099). The client deletes nothing.

## Files

- **FILE-1.** `PUT .../files` writes the files of the body, `{"files": [{"path": <path>, "content": <base64>}, ...]}`, each whole, through interface 3's `WriteFile`.
  - **The paths.** A path `p` is taken only where `path.Clean(p) == p`, `p` begins with `TokenDir`, `/run/garam-bot/`, and `p` is longer than it. Any other path MUST be answered 400 `invalid`, and nothing of the body is written.
  - **Links.** The server MUST NOT follow a symbolic link at a path's final component: it replaces the link, never the file the link names. Code in the workspace can plant a link, and a token written through it would land where that code chose.
  - **The size.** The body holds 1 to 16 files. Content above 64 KiB in all MUST be answered 413 `invalid`.
  - **The order.** The files are written in the body's order. A failure answers which path it reached, `{"error": ..., "path": <path>}`, and the files before it stay written.
  - **The answer.** It is 204 once every file is written. A workspace that is not running is answered 409 `not_running`.
- There is no read. The client knows what it wrote.

## Dial

- **DIAL-1.** The dial is an HTTP/1.1 upgrade alone.
  - **The protocol.** The client MUST offer only `http/1.1` by ALPN on the connection it dials on. An upgrade does not exist in HTTP/2.
  - **The request.** `GET .../dial` carries `Connection: Upgrade` and `Upgrade: workspace-access-dial/v1alpha1`, `DialProtocol`, and no body. Any other upgrade token is answered 400 `invalid`. The request names no port.
  - **The answer.** Once the server holds a connection to interface 2's `Port`, 7700, in the workspace, it answers `101 Switching Protocols` with `Connection: Upgrade` and `Upgrade: workspace-access-dial/v1alpha1`.
  - **The splice.** From then on the server MUST pass bytes both ways unchanged until either side closes, and MUST NOT read or log them.
  - **A workspace that is not running** is answered 409 `not_running`.
- **Authentication inside.** The connection authenticates nothing. The adapter's own token, interface 2's AUTH-1, is what the adapter takes.
- **DIAL-2.** A connection broken by the server, a restart among them, MUST be dialled again by the client after a back-off from 1 second, doubling to 60, reset by a 101. The adapter keeps its session and events, so the client reads on from the last event it read.
- **DIAL-3.** The server MUST bound its dials:
  - at most 4 open at once for a workload instance, a dial past it answered 429 `temporarily_unavailable`;
  - at most the place's configured number open in all, a dial past it answered 503 `temporarily_unavailable`;
  - a dial idle for 120 seconds closed. That is above interface 2's `MaxWait` of 50 seconds, so a long poll never idles a dial out.
- **DIAL-4.** The server MUST close an instance's open dials when the instance ends, when its bot is gone, and when the workspace stops or is deleted. A dial never outlives the authority it was opened under.

## Session tokens

- **TOK-1.** `POST .../session-tokens`, with no body, answers `SessionTokens`:
  - `model`: `{"token": <token>, "expiresIn": <seconds>}`;
  - `tools`: an object keyed by each tool name of WS-1's answer, each `{"token": <token>, "expiresIn": <seconds>}`.

  Each token lives at most an hour, and grants nothing outside its own service and this bot's workspace.
  - **The issuers.** The server obtains the tokens from the place's model gateway (interface 5) and tool proxy for the bot, and MUST NOT keep them past its answer.
  - **No issuer.** A server with no issuer configured MUST answer 503 `waiting_for_gateway`.
- **TOK-2.** The client MUST write the tokens into the workspace's token files with FILE-1, and MUST obtain and write new ones before two thirds of the shortest `expiresIn` have passed. So none in the workspace is more than an hour old (interface 2's CRED-1).

## Limits

- **LIMIT-1.** The server MUST limit, for each bot, the calls that reach a provider or an issuer: acquire, a write of files, a dial, and session tokens. A call past the limit MUST be answered 429 `temporarily_unavailable` with `Retry-After`, to that bot alone.
  - **What takes from it.** A call refused before a provider or an issuer is asked takes nothing, and no bot's calls take from another bot's. A read of the workspace (WS-2) takes nothing from it.
  - **Its size.** The limit is the server's to choose, and MUST leave room for a client's course: a renewal is one session tokens call and one write of files, and a reconnect one dial.
- **LIMIT-2.** The server MUST limit, for each workload instance, every request it takes. The check comes right after the credential verifies against keys the server holds, and before anything else is read for it. A request past the limit is answered 429 `temporarily_unavailable` with `Retry-After`, refused, 404 and 401 requests among them. So a process that sends refused requests costs no one but itself.
- **LIMIT-3.** DIAL-3's bounds are limits too.
- **REN-1.** A 429 or 503 carrying `Retry-After` MUST be waited at least that long, at most 60 seconds.

## Secrets

- **SEC-1.** Neither side may write a credential, a proof, a token, a file's content or the bytes of a dial to a log, a metric, an event, a status or a refusal: each MUST NOT. The server logs a call with the bot, the call and its outcome, and nothing more.

## Versioning and compatibility

- **A release of `v1alpha1` may add:**
  - a call that reaches nothing in a workspace REACH-1 names;
  - a member of an answer;
  - an error code;
  - a form of attachment WS-1 serves;
  - a phase.

  A client ignores an answer member it does not know (T-2), and reads a phase it does not know as `unknown` (VER-2).
- **REACH-1.** The interface's reach into a workspace is FILE-1's writes under `TokenDir` and DIAL-1's one port, and nothing else. A call that runs a command in a workspace, reads anything from it, writes outside `TokenDir` or reaches another port needs a new version and a decision record. A release of this version MUST NOT add one.
- **A new version is required** to:
  - remove or rename anything, or change a path;
  - widen REACH-1;
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
