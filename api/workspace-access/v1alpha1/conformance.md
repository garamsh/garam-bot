# Workspace access v1alpha1 conformance

The tests of `README.md`'s requirements: which suite holds each, and how an implementation runs it. The suite, its fixture and the reference server are built with the connector's side (garam-bot#1281). Until then every case is named here and none runs.

## The suite

- **The server suite** is to be `conformance/server/`, behind the build tag `conformance`, reaching a server only through the protocol and a fixture command, as interface 6's server suite does.
  - **The reference.** It runs against `reference/` in `make ci`.
  - **The cluster's connector.** It runs against the connector with the end-to-end suite on kind (garam-bot#1252).
- **The client's rules** are KEY-1, AUTH-2, WS-3, TOK-2, DIAL-2, REN-1 and VER-2. `internal/bot`'s tests hold them for `garam-bot run`, until a client suite runs a client against the reference server.

### The fixture

The fixture is a command whose subcommands each print one JSON object, as interface 6's does. It models what the server trusts.

| Capability | What it gives the suite |
|---|---|
| A bot | A bot placed at the place with its attachments, by namespace and name, with an identity that changes when it is made again under the same name |
| A workload instance | An instance of a bot: one the place runs, one restarted with a new instance identity, one ended, and a workload that runs no bot |
| A credential | One naming an instance, of the server's audience or another, expired, too long, or issued before the server started |
| A workspace's files | What a workspace holds at a path, so a write is checked without interface 7 reading |
| A workspace's state | Stopping, failing and deleting a workspace behind the server, and holding one pending |
| A clock | Moving the server's clock, for PROOF-1's window and the idle timeout |
| An echo adapter | A listener on interface 2's port that echoes, so a dial's bytes are checked |
| An issuer | A stub issuer of session tokens, present or absent |

## Requirements and their tests

Every case below is pending.

| Requirement | Tests |
|---|---|
| T-1 | `T-1/the server serves TLS 1.3 and the client trusts the place's bundle alone` |
| T-2 | `T-2/a request member the server does not know is refused` |
| T-3 | `T-3/a refusal is an error body with a known code` |
| CRED-2 | `CRED-2/a missing, expired, foreign or long credential is refused`, `CRED-2/a credential issued before the server started is refused`, `CRED-2/a credential of an instance that ended is refused`, `CRED-2/a credential is reused while it lives` |
| PROOF-1 | `PROOF-1/a proof for another method, URL, origin or credential is refused`, `PROOF-1/a proof 60 seconds from the clock is taken and 61 is refused`, `PROOF-1/a jti seen within 120 seconds is refused` |
| PIN-1 | `PIN-1/a second key for one instance is refused with invalid_key`, `PIN-1/a restarted instance pins its new key`, `PIN-1/a new instance of a remade workload pins its key`, `PIN-1/a pin is dropped with its instance` |
| BOT-1 | `BOT-1/a workload that runs no bot is unknown`, `BOT-1/an attachment of another bot is not found`, `BOT-1/a bot made again under its name reaches a new workspace and not the old` |
| AUTH-2 | the client's, in `internal/bot`'s tests |
| WS-1 | `WS-1/acquire is idempotent and answers the workspace's state`, `WS-1/acquire starts a stopped workspace and recovers a failed one with its data`, `WS-1/an attachment the server cannot serve is unsupported`, `WS-1/a workspace still deleting is answered deleting` |
| WS-2 | `WS-2/a read before acquire is not found`, `WS-2/a read takes nothing from the bot's limit` |
| WS-3 | `WS-3/a pending workspace is answered with Retry-After` |
| FILE-1 | `FILE-1/files under the token directory are written whole and in order`, `FILE-1/the token directory itself, a sibling, a dot-dot and an unclean path are refused and nothing is written`, `FILE-1/a link at the final component is replaced, not followed`, `FILE-1/content above 64 KiB is refused`, `FILE-1/a workspace not running is answered not_running` |
| DIAL-1 | `DIAL-1/a dial reaches the adapter's port and passes bytes unchanged`, `DIAL-1/another upgrade token is refused`, `DIAL-1/a workspace not running is answered not_running` |
| DIAL-3 | `DIAL-3/a fifth open dial of one instance is answered 429`, `DIAL-3/the server's cap is answered 503`, `DIAL-3/a dial idle for 120 seconds is closed, and one idle for 50 is not` |
| DIAL-4 | `DIAL-4/a dial closes when its instance ends`, `DIAL-4/a dial closes when its bot is gone`, `DIAL-4/a dial closes when its workspace stops` |
| TOK-1 | `TOK-1/session tokens name the model and every tool`, `TOK-1/a server with no issuer answers waiting_for_gateway` |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone`, `LIMIT-1/a call refused before a provider is asked takes nothing` |
| LIMIT-2 | `LIMIT-2/an instance past its request limit is answered 429, refused requests counted` |
| Deletion | `WS-1/a workspace whose bot is gone is deleted after the place's grace, and one whose bot lives is not` |
| SEC-1 | the connector's tests, which read its log for a credential, a token, a file's content or a dial's bytes |
| REACH-1 | read by review: no call reaches past `TokenDir` and the adapter's port |
| KEY-1, TOK-2, DIAL-2, REN-1, VER-2 | `internal/bot`'s tests, until a client suite |
| VER-1 | `SpecRelease` in this package, read by review: the release is a constant, not behaviour |
