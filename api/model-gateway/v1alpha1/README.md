# Model gateway v1alpha1

The normative specification of garam-bot's interface 5, the model gateway, at version `v1alpha1`: the protocol by which a workspace's harness calls a model through its place's gateway with a short-lived token, and by which the place's issuer obtains that token from the gateway for a bot. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Transport
- The issue surface
- The model surface
- Keys and billing
- Usage
- Limits
- Secrets
- Versioning and compatibility
- Open questions

## Scope and implementations

- **Two callers, one gateway** (ADR 0044, garam ADR-0190 decision 1):
  - **the model surface** is called by a workspace's harness, through interface 2's `model.baseUrl` and `model.tokenFile`;
  - **the issue surface** is called by the place's issuer of session tokens, interface 7's TOK-1, which hands the token on to the bot's process.
- **The gateway holds the provider's key.** It sets the key on every call it forwards, and no key reaches a workspace, a harness, an answer or a log (garam ADR-0190 decision 2).
- **What the protocol carries:** a model token for a bot, and that bot's model calls in the provider's own API. It routes no model, and holds no conversation.
- **The package is the schema:** its constants name `SpecRelease`, the paths, the headers, the error codes and the bounds (`doc.go`, `gateway.go`), and its types are the issue surface's bodies.

| Implementation | Place | State |
|---|---|---|
| `gatewayfake` | none: it issues and checks its own tokens, forwards to an upstream it is given with a key it is given, and records each model request it forwards | the reference implementation, built, passing the server suite; the stub interface 2's MODEL-1 case runs against |
| `cmd/model-gateway` | a user's cluster, beside the connector | first production implementation; to build (garam-bot#1211) |
| `ai.garam.sh` | garam's cloud | garam's implementation, to build (garam ADR-0190); its token is garam's exchange of a bot's token, which TOKEN-1 admits (garam ADR-0195) |

## Transport

- **T-1.** The gateway MUST serve HTTPS, TLS 1.2 at least. A caller MUST verify the gateway's certificate against the trust its place gives it.
- **T-2.** The issue surface is under `PathPrefix`, `/model-gateway/v1alpha1/`. The model surface is at the provider's own paths, below the base URL interface 2's `model.baseUrl` names.
- **T-3.** Bodies of the issue surface are JSON. The gateway MUST refuse a request body naming a member it does not know with 400 `invalid`, and both sides MUST ignore a member they do not know in an answer. Every refusal of the issue surface is `ErrorBody`, `{"error": <code>, "error_description": <text>}`, with one of `Code`'s codes. A failure of the gateway's own is answered 503 `temporarily_unavailable`.

## The issue surface

| Call | Meaning |
|---|---|
| `POST .../model-tokens` | A model token for a bot (ISS-2) |

| Status | Code | Meaning |
|---|---|---|
| 200 | — | `ModelToken` (ISS-2) |
| 400 | `invalid` | A body or member the specification refuses (T-3, ISS-2) |
| 401 | `invalid_credential` | The issuer's credential is missing, does not verify, has expired, names another audience, or lives longer than an hour (ISS-1) |
| 403 | `forbidden` | The credential verifies and names no issuer the gateway serves (ISS-1) |
| 429 | `temporarily_unavailable` | The issuer's limit is reached: try again after `Retry-After` (LIMIT-2) |
| 503 | `temporarily_unavailable` | The gateway cannot answer now: try again after `Retry-After` where it is given |

- **ISS-1.** Every call MUST carry `Authorization: Bearer <credential>`, a credential of the place naming its issuer, of the gateway's own audience.
  - **What the gateway refuses.** It MUST answer 401 `invalid_credential` to a credential that is missing or does not verify, has expired, names another audience, or lives longer than an hour. It MUST answer 403 `forbidden` to one that verifies and names no issuer it is configured to serve.
  - **At a cluster** the credential is the connector's projected ServiceAccount token, of the audience `garam-model-gateway`, checked against the API server's keys, and the issuer served is the connector's ServiceAccount.
- **ISS-2.** `POST .../model-tokens` with `TokenRequest`, `{"namespace": <ns>, "name": <name>, "uid": <uid>}`, the bot's identity as the issuer resolved it, answers `ModelToken`, `{"token": <token>, "expiresIn": <seconds>}`.
  - **Each member is required,** and each is at most 253 bytes. A body missing one is answered 400 `invalid`.
  - **The bot is the issuer's word.** The gateway does not resolve it; it names it in the token, and attributes the token's calls to it (USE-1).
- **TOKEN-1.** A model token:
  - lives at most an hour, and `expiresIn` is at most 3600;
  - is of the gateway's own audience, and is checked by that gateway alone;
  - names the bot of its request, and grants nothing but that bot's calls on the model surface;
  - is opaque to every other party. Its form is the gateway's.
- **TOKEN-2.** The gateway MUST go on accepting a token it issued, until the token expires, across its own restart. The bot's process renews its tokens after 40 minutes at most (interface 7's TOK-2), and a restart that voided them would leave every workspace without a model until then.

## The model surface

The surface is the provider's own API, passed through. This release names one provider API.

| Provider API | Paths | Errors |
|---|---|---|
| Anthropic Messages | `POST /v1/messages`, `POST /v1/messages/count_tokens` | `{"type": "error", "error": {"type": <type>, "message": <text>}}` |

- **MOD-1.** Every call MUST carry `Authorization: Bearer <model token>`. The gateway MUST answer 401, in the provider API's error form with the type `authentication_error`, to a call with no token, a token it did not issue, a token whose signature or form does not verify, or one that has expired.
- **MOD-2.** For a call it takes, the gateway MUST:
  - forward it to its upstream at the same path and query, with the same method and body;
  - forward none of the call's headers but `ForwardedHeaders`: `Content-Type`, `Accept`, and the provider API's version and feature headers, `Anthropic-Version` and `Anthropic-Beta`. So the call's `Authorization`, `X-Api-Key` and cookies reach no upstream;
  - set the provider's key as that provider's API takes it, `X-Api-Key` for Anthropic's;
  - answer with the upstream's status, body and headers, except `Set-Cookie` and the hop-by-hop headers;
  - pass a streamed answer (`text/event-stream`) on as each part arrives, unbuffered.
- **MOD-3.** A path the provider API table does not name is answered 404 in the provider API's error form with the type `not_found_error`, and is forwarded nowhere.
- **MOD-4.** A request body above `MaxRequestBody`, 32 MiB, is answered 413 in the provider API's error form with the type `request_too_large`, and is forwarded nowhere.
- **MOD-5.** The gateway MUST NOT follow a redirect its upstream answers, nor pass it on: it is answered 502 in the provider API's error form with the type `api_error`, so no `Location` of the upstream's reaches the harness. So is an upstream it cannot reach, or one that does not begin its answer within `UpstreamHeaderTimeout`, 10 minutes.

## Keys and billing

- **KEY-1.** The gateway MUST hold every provider key it uses outside every workspace, and MUST NOT write one to a log, a metric, an answer or a report.
- **KEY-2.** This release serves bring-your-own-key alone (cupa, garam-bot#1211, 2026-11-28; garam ADR-0190 decision 4). The organization's own provider key is the one the gateway sets. A gateway with no key configured MUST NOT serve the model surface. Where a platform pays on an organization's behalf is a later release's, through a key or an upstream the gateway chooses per bot, behind the same two surfaces.

## Usage

- **USE-1.** The gateway MUST record, for every call of the model surface it forwards, the bot its token names, the model the call names, the upstream's status, and the input and output tokens the provider's answer reports. A record holds nothing of the call's content. How a deployment keeps the records, in logs, metrics or both, is its own.

## Limits

- **LIMIT-1.** The gateway MUST limit each bot's calls of the model surface. A call past the limit is answered 429 in the provider API's error form with the type `rate_limit_error`, with `Retry-After`, and is forwarded nowhere. No bot's calls take from another bot's.
- **LIMIT-2.** The gateway MUST limit the issue surface's calls of each issuer. A call past the limit is answered 429 `temporarily_unavailable` with `Retry-After`.
- **Their size** is the gateway's to choose, and MUST leave room for a harness's course of calls and an issuer's renewals.

## Secrets

- **SEC-1.** Neither side may write a provider key, a model token, an issuer's credential or a call's content to a log, a metric, an event, a status or a refusal: each MUST NOT. A refusal names its reason, never the token or the credential.

## Versioning and compatibility

- **A release of `v1alpha1` may add:**
  - a provider API to the model surface, with its paths and error form;
  - a member of an answer;
  - an error code.

  A caller ignores an answer member it does not know (T-3).
- **A new version is required** to:
  - remove or rename anything, or change a path;
  - make a request member or header required;
  - change what a status means;
  - widen TOKEN-1's life or reach.

  A new version is served at a new path, beside the earlier one while callers move.
- **A new requirement of the gateway** that no conforming caller relies on, a tightening, is a release. One a caller must act on is a new version.
- **VER-1.** Each release has a number, `SpecRelease` in this package, from `0.1.0`, and the `api/` module's tag moves with every release.
- **A deprecated element** is marked so here and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the suite: the place's gateway and garam's at `ai.garam.sh`.

## Open questions

- **Usage reported to garam.** A place's gateway reports no record to garam (garam ADR-0190 §Consequences). Whether interface 5 or interface 4 carries one, for garam's budgets and spend, is not decided.
- **Other provider APIs.** OpenAI's, for the codex adapter, is a later release's (5.10).
- **Platform billing.** Which key or upstream a gateway uses where garam pays on an organization's behalf waits for that agreement (KEY-2).
- **A token bound to its workspace.** A model token is a bearer within its hour. Binding it to the workspace's network identity is not decided.
