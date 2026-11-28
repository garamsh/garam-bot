# Model gateway v1alpha1 conformance

The tests a gateway implementing `README.md` must pass, each named and mapped to the requirements it holds. T-1's verification and VER-1 are the callers' and the package's, and no suite holds them. KEY-1 and SEC-1 are held as far as an answer shows them: no answer carries the provider's key. What a gateway writes to its logs is held by each implementation's own tests.

## The suite

The suite reaches a gateway only through its two surfaces and the fixture below, never through its internals. It is `conformance/server/` beside this file, a Go test behind the build tag `conformance`.

- `CONFORMANCE_BASE_URL` is the gateway's base URL, the one interface 2's `model.baseUrl` names. `CONFORMANCE_TRUST_FILE` is a PEM file of the certificates the gateway is trusted by (T-1), and `CONFORMANCE_FIXTURE` the fixture command, split on white space.
- `go test -tags=conformance ./model-gateway/v1alpha1/conformance/server/` runs it from `api/`. `make conformance-model-gateway`, a stage of `make ci`, runs it against `gatewayfake`, served by `gatewayfake/cmd/model-gateway-reference`.
- **The upstream is the fixture's.** The gateway under test forwards to a stand-in for the provider the fixture runs, `gatewayfake.Provider`, so the suite reads what reached the upstream and scripts its answers.
- **The cases run one after another**, each with bots of its own. The gateway's own limits are read from the fixture, never assumed.

### The fixture

Each subcommand exits 0 and prints its answer as one JSON object, or exits non-zero and prints why.

| Subcommand | Does | Prints |
|---|---|---|
| `credential [--issuer <name>] [--audience <a>] [--expired] [--lifetime <duration>]` | Issues the place's credential naming the issuer the gateway serves, or the issuer named. `--expired` is issued and expired since; `--lifetime` sets its life. | `credential`, `issuer` |
| `provider-key` | The provider's key the gateway is configured with. | `key` |
| `seen` | Every call the upstream has received, in order. | `seen`: each `method`, `path`, `query`, `header`, `body` in base64 |
| `answer [--status <n>] [--stream] [--redirect] [--close]` | Scripts the upstream's answer to its next call: a status, a stream of two events whose second waits for `release`, a redirect, or a hang-up. Unscripted, it answers a whole Messages answer reporting its usage. | `{}` |
| `release` | Lets a held stream send its second event. | `{}` |
| `usage` | The gateway's usage records (USE-1). | `usage`: each `namespace`, `name`, `uid`, `model`, `status`, `inputTokens`, `outputTokens` |
| `restart` | Restarts the gateway, as its place restarts it, and returns once it serves again (TOKEN-2). | `{}` |
| `clock --advance <duration>` | Moves the gateway's clock, for tokens' and credentials' expiry and for its limits. | `{}` |
| `limits` | The gateway's limits: each bot's and each issuer's burst, and the seconds each takes to give one call back (LIMIT-1, LIMIT-2). | `botBurst`, `botEverySeconds`, `issuerBurst`, `issuerEverySeconds` |

## Requirements and their tests

Each test is a subtest of `TestServer`, named `<requirement>/<case>`.

| Requirement | Tests |
|---|---|
| T-1 | Every case: the suite reaches the gateway over TLS, trusting `CONFORMANCE_TRUST_FILE` alone |
| T-2, T-3 | `ISS-2/a request missing a member, naming one it does not know, or above the bound is refused` |
| ISS-1 | `ISS-1/an issuer's credential that is missing, does not verify, has expired, names another audience or lives past an hour is refused`, `ISS-1/a credential naming no issuer the gateway serves is forbidden` |
| ISS-2 | `ISS-2/a request missing a member, naming one it does not know, or above the bound is refused` |
| TOKEN-1 | `TOKEN-1/a token lives at most an hour and is refused once it expires` |
| TOKEN-2 | `TOKEN-2/a token issued before a restart is taken after it` |
| MOD-1 | `MOD-1/a call with no token, a token not issued, or one altered is refused` |
| MOD-2 | `MOD-2/a call is forwarded with its path, query and body, its own credentials removed and the provider's key set`, `MOD-2/a streamed answer is passed on as each part arrives` |
| MOD-3 | `MOD-3/a path the provider API does not name is refused and forwarded nowhere` |
| MOD-4 | `MOD-4/a body above 32 MiB is refused and forwarded nowhere` |
| MOD-5 | `MOD-5/an upstream redirect or hang-up is answered 502 and not followed` |
| KEY-1, SEC-1 | `MOD-2/a call is forwarded…` and `MOD-5/…`: no answer's headers or body carry the provider's key |
| KEY-2 | `MOD-2/a call is forwarded…`: the key the upstream receives is the one configured |
| USE-1 | `USE-1/each forwarded call is recorded for the bot its token names, with the provider's usage` |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone` |
| LIMIT-2 | `LIMIT-2/an issuer past its limit is answered 429` |

## Interface 2's MODEL-1

`gatewayfake.Gateway` is also the stub interface 2's suite runs MODEL-1 against (`api/agent/v1alpha1/conformance.md`): it records each model call it forwards, with the bot its token names, and counts each it refuses.
