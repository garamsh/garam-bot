# Workspace access v1alpha1 conformance

The tests of `README.md`'s requirements: which suite holds each, and how an implementation runs it. The suite, its fixture and the reference server are built with the connector's side (garam-bot#1281). Until then every case is named here and none runs.

## The suite

- **The server suite** is to be `conformance/server/`, behind the build tag `conformance`, reaching a server only through the protocol and a fixture command, as interface 6's server suite does.
  - **The fixture** issues the place's credentials, places bots with their attachments, and reads what a workspace holds.
  - **The reference.** It runs against `reference/` in `make ci`.
  - **The cluster's connector.** It runs against the connector with the end-to-end suite on kind (garam-bot#1252).
- **The client's rules** are KEY-1, TOK-2, DIAL-2, REN-1 and VER-2. `internal/bot`'s tests hold them for `garam-bot run`, until a client suite runs a client against the reference server.

## Requirements and their tests

| Requirement | Tests |
|---|---|
| T-1 | `T-1/the server serves TLS 1.3 and the client trusts the place's bundle alone`, pending |
| T-2, T-3 | `T-3/a refusal is an error body with a known code`, pending |
| CRED-1, CRED-2 | `CRED-2/a missing, expired, foreign, long or pre-start credential is refused`, pending. The workload binding is the connector's tests' |
| PROOF-1 | `PROOF-1/a proof for another method, URL or credential, or a replayed one, is refused`, pending |
| PIN-1 | `PIN-1/a second key for one instance is refused`, pending |
| BOT-1 | `BOT-1/an attachment of another bot is not found`, pending |
| WS-1, WS-2 | `WS-1/acquire is idempotent and answers the workspace's state`, `WS-1/an attachment the server cannot serve is unsupported`, pending |
| FILE-1 | `FILE-1/a file under the token directory is written whole`, `FILE-1/a path outside it is refused`, pending |
| DIAL-1 | `DIAL-1/a dial reaches the adapter's port and passes bytes unchanged`, pending |
| TOK-1 | `TOK-1/session tokens are answered, or waiting for the gateway`, pending |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone`, pending |
| SEC-1 | the connector's tests, which read its log for a credential, a token or a file's content |
| KEY-1, TOK-2, DIAL-2, REN-1, VER-2 | `internal/bot`'s tests, until a client suite |
| VER-1 | `SpecRelease` in this package, read by review: the release is a constant, not behaviour |
