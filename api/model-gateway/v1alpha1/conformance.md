# Model gateway v1alpha1 conformance

The tests a gateway implementing `README.md` must pass, each named and mapped to the requirements it holds. T-1's verification and VER-1 are the callers' and the package's, and no suite holds them. KEY-1 and SEC-1 are held as far as an answer shows them: no answer carries the provider's key. What a gateway writes to its logs is held by each implementation's own tests.

## The suite

The suite reaches a gateway only through its two surfaces and the fixture below, never through its internals. It is `conformance/server/` beside this file, a Go test behind the build tag `conformance`.

- **The environment.**
  - `CONFORMANCE_BASE_URL` is the gateway's base URL, the one interface 2's `model.baseUrl` names, with its path where it has one (T-2).
  - `CONFORMANCE_TRUST_FILE` is a PEM file of the certificates the gateway is trusted by (T-1).
  - `CONFORMANCE_FIXTURE` is the fixture command, split on white space.
  - `CONFORMANCE_PROFILE` is `place` or `model` (PRO-1, PRO-2). Empty is `place`.
- **The profiles.** A run of the model profile skips the place profile's cases, each naming PRO-2. It takes its tokens from the fixture's `token`, where a run of the place profile takes them from the issue surface.
- **Running it.** `go test -tags=conformance ./model-gateway/v1alpha1/conformance/server/` runs it from `api/`.
  - `make conformance-model-gateway`, a stage of `make ci`, runs it twice against `gatewayfake`, served by `gatewayfake/cmd/model-gateway-reference`.
  - The first run is the place profile, with the base URL below `/gateway`. The second is the model profile, at the root.
- **The upstream is the fixture's.** The gateway under test forwards to a stand-in for the provider the fixture runs, `gatewayfake.Provider`, so the suite reads what reached the upstream, scripts its answers and knows the counts it reports. A scripted redirect points at a second stand-in, a `gatewayfake.Recorder`, so the suite reads whether a call reached it.
- **The cases run one after another**, each with bots of its own. The gateway's own limits are read from the fixture, never assumed.

### The fixture

Each subcommand exits 0 and prints its answer as one JSON object, or exits non-zero and prints why. A run of the model profile needs no `credential` and no `restart`.

| Subcommand | Does | Prints |
|---|---|---|
| `credential [--issuer <name>] [--audience <a>] [--expired] [--lifetime <duration>] [--foreign]` | Issues the place's credential naming the issuer the gateway serves, or the issuer named. `--expired` is issued and expired since. `--lifetime` sets its life. `--foreign` is signed by a key the gateway does not trust for its place. | `credential`, `issuer` |
| `token --namespace <ns> --name <name> --uid <uid> [--foreign]` | A model token for that bot, as the gateway's issuer would obtain it. `--foreign` is well formed and signed by another key. | `token`, `expiresIn` |
| `provider-key` | The provider's key the gateway is configured with. | `key` |
| `seen [--redirected]` | Every call the upstream has received, in order, or with `--redirected` every call a redirect's `Location` has. | `seen`: each `method`, `path` as it was sent, `query`, `header`, `body` in base64 up to a MiB, `size` |
| `answer [--status <n>] [--retry-after <s>] [--stream] [--long] [--break] [--redirect <status>] [--close]` | Scripts the upstream's answer to its next call. | `{}` |
| `release` | Lets a held stream send its last event. | `{}` |
| `usage` | The gateway's usage records (USE-1). | `usage`: each `namespace`, `name`, `uid`, `model`, `status`, `inputTokens`, `outputTokens`, `cacheCreationInputTokens`, `cacheReadInputTokens` |
| `restart` | Restarts the gateway, as its place restarts it, and returns once it serves again (TOKEN-2). | `{}` |
| `clock --advance <duration>` | Moves the gateway's clock, for tokens' and credentials' expiry and for its limits. | `{}` |
| `limits` | The gateway's limits: each bot's and each issuer's burst, and the seconds each takes to give one call back (LIMIT-1, LIMIT-2). | `botBurst`, `botEverySeconds`, `issuerBurst`, `issuerEverySeconds` |

`answer`'s scripts:

- unscripted, a whole Messages answer reporting each of its counts;
- `--status` answers that status, with `--retry-after`'s `Retry-After`;
- `--stream` is a stream whose last event waits for `release`, and `--long` pads it past a MiB before its last event;
- `--break` hangs the stream up after its first event;
- `--redirect` answers that 3xx, its `Location` at the Recorder;
- `--close` hangs up with no answer.

## Requirements and their tests

Each test is a subtest of `TestServer`, named `<requirement>/<case>`. The place profile's cases are those of ISS-1, ISS-2, ISS-3, TOKEN-2 and LIMIT-2, and T-4's issue-surface half.

| Requirement | Tests |
|---|---|
| T-1 | Every case: the suite reaches the gateway over TLS, trusting `CONFORMANCE_TRUST_FILE` alone |
| T-2 | Every case: the place profile's run has a base URL with a path, and MOD-3 sends its paths uncleaned and undecoded |
| T-3 | `ISS-2/a request missing a member, naming one it does not know, naming one twice, not one object, or above the bound is refused` |
| T-4 | `T-4/the Bearer scheme is taken in any case` |
| PRO-1, PRO-2 | `make conformance-model-gateway`'s two runs |
| ISS-1 | `ISS-1/an issuer's credential that is missing, not signed by a key the place trusts, altered, expired, of another audience or past an hour is refused`, `ISS-1/a credential naming no issuer the gateway serves is forbidden` |
| ISS-2 | `ISS-2/a request missing a member, …` |
| ISS-3 | `ISS-3/the issue surface refuses its path and method, then the credential, then the issuer, then the body` |
| TOKEN-1 | `TOKEN-1/a token lives at most an hour and is refused once it expires` |
| TOKEN-2 | `TOKEN-2/a token issued before a restart is taken after it`. Its rotation rule has no case: a gateway rotates on its own schedule, which the fixture does not reach |
| MOD-1 | `MOD-1/a call with no token, a token not issued, one of another key or one altered is refused` |
| MOD-2 | `MOD-2/a call is forwarded with its path, query and body, no header but the provider API's own, the provider's key and the transport's`, `MOD-2/an upstream's refusal is passed on with its status and Retry-After`, `MOD-2/a streamed answer is passed on as each part arrives` |
| MOD-3 | `MOD-3/a method and path the provider API table does not name are refused and forwarded nowhere` |
| MOD-4 | `MOD-4/a body above 32 MiB is refused and forwarded nowhere, and one of 32 MiB is forwarded` |
| MOD-5 | `MOD-5/an upstream redirect is answered 502 and no call reaches its Location, nor does a hang-up` |
| MOD-6 | `MOD-6/an upstream that hangs up in the middle of a stream breaks the answer off` |
| MOD-7 | `MOD-7/the model surface refuses the token, then the method and path, then the limit, then the size` |
| KEY-1, SEC-1 | `MOD-2/a call is forwarded…` and `MOD-5/…`: no answer's headers or body carry the provider's key |
| KEY-2 | `MOD-2/a call is forwarded…`: the key the upstream receives is the one configured |
| USE-1 | `USE-1/each forwarded call is recorded for the bot its token names, with each of the provider's counts`, `USE-1/a stream past a MiB is recorded with the counts its last events report` |
| LIMIT-1 | `LIMIT-1/a bot past its limit is answered 429 alone, and forwarded nowhere` |
| LIMIT-2 | `LIMIT-2/an issuer past its limit is answered 429` |

## Interface 2's MODEL-1

`gatewayfake.Gateway` is also the stub interface 2's suite runs MODEL-1 against (`api/agent/v1alpha1/conformance.md`). It records each model call it forwards, with the bot its token names, and counts each it refuses.
