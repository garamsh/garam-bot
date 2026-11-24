# Bot system

The whole bot system across its repositories:
- where a bot is declared and where it runs;
- how each execution place connects to garam, and how every party authenticates;
- where keys live, and how workspaces, agents and external apps are reached;
- the repository layout, and what exists and what is to build, per repository.

This file governs the shape that crosses repositories. Each interface's own specification is under `api/`, and each repository's own documents govern its inside.

Every claim about another repository below holds at the revision it names:
- garam `617686ff296df67b22c23699555310949f00861d`;
- garam-bot-operator `b66fb8553ce7f4842e97986b4d7303e5eed327c8`;
- naru `34d17abf0f513a0ea38e1077ea48f92e41c5d413`;
- mvmd `196b7a455e4d6a50c2333c718d8333f4e3f94739`;
- codehub `d738c411d7d6720b3801516cfc9475163cd1c728`;
- aurora `c26fd899cab93a23b0a6ab2e0afa14e87868b4bc`.

## Current decisions

### Declare anywhere, manage in garam

- **One schema, three places to declare** (ADR 0036). Interface 1, `bot.garam.sh` at `v1alpha1`, reads the same from a file and a CRD (ADR 0032). A bot is declared:

  | Where | Owner | Marker |
  |---|---|---|
  | garam's dashboard or API, placed on one execution place | garam | `bot.garam.sh/managed-by: garam`, the place's GRN and the declaration's identifier and generation |
  | a CRD in a cluster | the cluster | none, or `cluster` |
  | a file at a PC or a naru host | that place | none, or `cluster`, which names the place's own objects at every kind of place |

- **garam shows every bot of every place** from the place's reports, and changes only what it owns. A place applies garam's declarations and never changes its own objects on garam's word (the cluster-connection protocol's DECL-11 to DECL-14).
- **A bot runs without garam** (ADR 0031). A place that loses garam keeps running what it runs, and a bot declared at its place needs no garam.

### Execution places and their connectors

- **An execution place is a cluster, a PC or a naru host, and each runs one connector, registered once** (ADR 0036, ADR 0039).

  | Place | Connector | Installed by | A bot's process runs as | Workspace providers (interface 3) |
  |---|---|---|---|---|
  | Kubernetes cluster | the connector in `operator/`, beside its manager (garam-bot-operator ADR 0090) | its Helm chart | one StatefulSet of one, the runtime image (ADR 0033) | Pod; naru Machine where the cluster reaches a naru fleet |
  | Linux PC | `garam-bot daemon` | a system package, a systemd service | a child `garam-bot run` | container, and mvmd with KVM |
  | Mac | `garam-bot daemon` | a package, a launchd service | a child `garam-bot run` | container |
  | naru host | `garam-bot daemon` | naru's host configuration | a child `garam-bot run` | naru Machine |

- **Registration, the same at every place:**
  - a one-time registration token, minted in garam for the place, valid one hour, spent on its first use, with only its hash kept (garam ADR-0184);
  - a key the connector makes, RSA 3072 or more, which never leaves the place: a Secret in a cluster, the operating system's key store or an owner-only file on a PC or a host;
  - the connector rotates it itself, every 30 days in garam-bot-operator `docs/architecture/connector.md`, a period its ADR 0094 makes `--rotation-period` (reserved for garam-bot-operator#439);
  - revoking a key or deleting the place in garam refuses its next request.
- **After registration, garam chooses which bot runs where.** A bot declared in garam is placed on one place, and that place's connector takes it in its declaration feed and runs it.
- **One protocol for every place.** Interface 4 is the cluster-connection protocol, `v1alpha1` today (garam-bot-operator `interfaces/cluster-connection/v1alpha1/`, moving to `api/cluster-connection/v1alpha1/` with garam-bot#1205). It widens to every place by a new version carrying the place's kind. A PC or a host holds one namespace and no namespace claim.

### Pull only

- **Every connection is opened by the place** (ADR 0036). garam opens no connection to a place, and a place opens no inbound port (garam-bot-operator ADR 0087, ADR 0091; the protocol's T-1).
- **Declarations** are long-polled with a cursor, tombstones and a purge point (the protocol's DECL-1 to DECL-6; garam ADR-0187).
- **Reports** are pushed by the place, as its whole view, within 10 seconds of a change and every 5 minutes (the protocol's REP-1 to REP-9).
- **Messages to a bot** reach its process by long poll or stream that the bot's process opens with its token. garam's mailbox long poll for a bot is garam's to build.
- **naru** is reached the same way. garam declares and never calls naru's Machine API (garam#2042). The Machine API's caller is garam-bot's naru Machine provider, at the place (naru#409, naru#378).

### Authentication

The standards are ADR 0037's.

| Party | Standard | Token | What exists | What is to build |
|---|---|---|---|---|
| A person in the console | OIDC authorization code with PKCE | garam's session | garam `docs/architecture/sign-in.md`, Google as provider | — |
| The CLI | RFC 8628 device authorization | a session held as a bearer | garam ADR-0120 | — |
| The PC app | RFC 8628 device authorization | a session held as a bearer | — | goal ⑤, on the CLI's endpoints |
| A connector | `private_key_jwt` (RFC 7523) | one hour, read on every request | garam ADR-0184, garam#2046 | the PC and host place records (§Goal ④ widened, G1) |
| A bot | RFC 8693 token exchange by its connector: the connector's token as the subject token, the bot named as the audience, the place as the actor | at most one hour, kept at the place, never in a workspace | garam#2049 is the mint, not built | garam#2049 as an RFC 8693 exchange; garam-bot#1207 |
| A tool or a model, in a workspace | narrower tokens the bot's process obtains | at most one hour, in token files (interface 2's CRED-1) | — | interface 5; the tool proxy (§External apps, B7) |
| A first-party service (cowork, codehub) | `private_key_jwt` with a key in Transit | at most 300 seconds, for the policy service | garam ADR-0129, `docs/architecture/machine-identity.md` §cowork's principal | garam#2061, codehub#327 |
| A naru caller | a certificate the signer issues, renewed by its holder | 90 days at most | `naructl caller credential`, by hand | naru#409 |

- **Signing keys are split by purpose**, each rotated automatically and published as JWKS by the issuer it serves:
  - garam's OIDC issuer;
  - its policy feed;
  - its message signing;
  - codehub's registry tokens;
  - each first-party service's assertion key;
  - naru's authorities.

  garam's OIDC issuer signs with the policy feed's key today (garam `docs/architecture/deployment-contract.md` §The identity issuer is off until it is named), which garam#2061 splits.
- **Internal TLS is issued automatically:**
  - cert-manager in a cluster, with an OpenBao PKI issuer where a CA is shared;
  - naru's signer on a naru host, renewed by the holder;
  - ACME for a public name.

### Key management

- **The target** (sauce, `garamsh/discussions` discussion 6, comment 19857399; ADR 0037):
  - **Nothing made by hand.** No key is made, placed or renewed by a person. A person handles the AWS KMS root, naru's recovery path and external service tokens (GitHub, Proxmox, Cloudflare), and nothing else.
  - **KEKs and signing keys** are held in OpenBao Transit or KMS, and never leave it.
  - **Internal TLS and CAs** come from cert-manager, or from OpenBao PKI where needed.
  - **The OIDC issuer** has its own key, rotates it, and publishes JWKS.
  - **The operator, the connector and the PC daemon** register with a token and a key made on site (garam-bot-operator ADR 0088).
  - **Shell key generation goes**, `deploy/charts/cluster/key-material.sh` first.
- **Ownership.** The code side is the PM's: garam, garam-bot and naru, naru's signer CA included, and codehub's. Ian owns aurora's OpenBao Transit, PKI and cert-manager setup.
- **The issues, in the order they land.** Each lands behind configuration that keeps today's path, so current build work is not blocked, and all finish by the registration step at the end of the build:
  1. garam#2061: Transit for every garam signing key and the KEK, the issuer's own key, cert-manager TLS, no operator PEM, no key scripts.
  2. naru#412, beside codehub#327:
     - naru#412: the signer's authorities in KMS, certificates renewed by their holders, ACME for the proxy;
     - codehub#327: the AuthZEN and registry keys in Transit, no shared secrets.
  3. garam-bot#1207: the manager and the control service through the connector, bot tokens by RFC 8693, no gateway TLS.
  4. aurora#864: Transit, PKI and cert-manager for every consumer. These can be set up at any time. The switch comes last, at the registration step, connecting aurora by the connector's registration token and deleting every hand-placed key.

  naru#409, the Machine API's callers registering themselves, is the PM's and lands beside naru#412.

### Workspaces and agents

- **One workspace interface, many implementations** (ADR 0034). Interface 3 is the Go driver interface `Provider`, `api/workspace/v1alpha1/` (garam-bot#1206):
  - its operations are create, get, list, start, stop, delete, exec, files, dial and snapshot;
  - its specification maps every operation, phase and error onto naru's `machines/v1alpha1`;
  - a workspace's state is one data volume at `/workspace`, and it sends nothing out but what its egress rules allow.

  | Place | Implementations |
  |---|---|
  | cluster | naru Machine, Pod |
  | Linux PC | container, mvmd |
  | Mac | container |
  | naru host | naru Machine |

- **The agent interface** (ADR 0035) is the HTTP protocol `agent/v1alpha1`, `api/agent/v1alpha1/` (garam-bot#1206):
  - an adapter in the workspace image serves it on port 7700, and the bot's process reaches it through `Dial`;
  - a session runs one harness: Claude Code, Codex or another adapter;
  - tasks carry the bot's mailbox, and events are long-polled. The bot's process keeps the bot's memory from them.
- **No secret enters a workspace** (interface 2's CRED-1). Its credentials are the adapter's token, the model gateway's token and the tool tokens. Each of the last two lasts at most an hour, and the bot's process rewrites it.
- **Every tool is an MCP server outside the workspace.** The bot's process, or its tool proxy at the place, adds the tool's own credential in one of three modes:
  - `garam`: §External apps;
  - `secretRef`: a Secret the declaration names, read at the place;
  - `federated`: a token exchanged at the tool's issuer.
- **Model calls go to interface 5**, the model gateway, with a token file the bot's process keeps current. Interface 5 is not written yet.

### External apps

- **garam brokers external apps** (ADR 0038; sauce, comment 19857097):
  - **one official app per external service,** GitHub first, installed once per organization; garam holds each installation as a connection;
  - **the scope lives in garam IAM:** a policy over GRNs names which bot may use which connection with which scope, and garam's policy service decides each request as an AuthZEN evaluation;
  - **short narrowed tokens:** a bot's tool proxy at the place exchanges the bot's token by RFC 8693 for one connection and one scope, and garam mints the service's narrowest token, at most an hour long. For GitHub that is an installation token with `repositories` and `permissions` set. garam calls the service, never the place;
  - **a person's OAuth** is used only when a task acts for that person, and only on that person's grant (garam#1787 decides how a token carries who a bot acts for);
  - **an organization may bring its own app,** under the same policy and exchange;
  - **the apps' private keys are held in Transit,** and sign the apps' JWTs there.
- **The issues it needs, not filed, in order** (garam's unless named):
  - B1. Connections: an organization installs garam's official GitHub App, and garam records the installation as a connection GRN, with console pages.
  - B2. The connection policy: IAM actions and a Cedar policy shape for "this bot may use this connection with this scope", evaluated through AuthZEN.
  - B3. The external token exchange: RFC 8693 at garam's token endpoint, a bot's token in, a GitHub installation token narrowed by `repositories` and `permissions` out. It needs B1, B2 and garam#2049.
  - B4. The apps' private keys in Transit, signing the app JWTs there. It needs garam#2061.
  - B5. A person's grant on that person's behalf: GitHub user-to-server tokens, sealed in Transit and refreshed by garam. It needs garam#1787.
  - B6. An organization's own app, registered with its key placed into Transit.
  - B7. garam-bot: the tool proxy at the place, interface 2's `garam` mode, which exchanges and caches each token within its life and adds it to the MCP server's calls. It needs B3. It supersedes garam-bot#840.
  - B8. sauce-level: registering garam's official GitHub App under the `garamsh` organization, and later each other service's app. Creating an app and its key is a person's act at the service, once.

### Repository layout

- **One repository per product** (sauce, comment 19857166; garam-bot#1205). Each component inside has its own Go module and version tags, so it can be published or split off alone.

  | Repository | Holds |
  |---|---|
  | garam-bot | the runtime and `garam-bot daemon` (root module `github.com/garamsh/garam-bot`), the interfaces (`api/`, tags `api/vX.Y.Z`), the cluster connector and manager (`operator/`, tags `operator/vX.Y.Z`, moved from garam-bot-operator by #1205), and the PC app (`app/`) |
  | garam | the platform server, the console and cowork, pinning a released `garam-bot/api` |
  | naru, mvmd | separate for now; whether to join them is decided once naru's goal ③ design settles |
  | codehub, aurora | unchanged |

- **`cmd/sherlock` goes** (ADR 0040):
  - `sherlock agent` and the in-house loop are removed;
  - the workspace server, its operations and its MCP server move under `garam-bot workspace`;
  - the memory store stays as the candidate for the bot's memory.

  It follows #1205's module rename.

### The PC daemon and the PC app

These are designed in goal ④ and built in goal ⑤ (sauce, comment 19857097; ADR 0039).

- **Ownership.** garam-bot owns both: `garam-bot daemon` in the root module, the PC app in `app/`.
- **Registration:**
  1. A person signs in to the PC app with the device grant.
  2. The app asks garam for a one-time token for a new PC place in the person's organization, and hands it to the daemon over the local socket.
  3. The daemon makes its key in the operating system's key store, or in an owner-only file, and registers.

  The daemon rotates its key itself, and the person never sees it.
- **Running several bots.** The daemon long-polls the declarations placed on its place and reads the place's own from a directory of files. For each Bot it runs one child `garam-bot run`, with that Bot's token, which the daemon exchanges (RFC 8693) and rewrites, and with workspaces from its providers:
  - container (Docker or Podman), or mvmd where the PC has KVM, on Linux;
  - container on a Mac.

  It reports the place's objects to garam as a cluster connector does.
- **The PC app** reads the daemon over a local socket only the PC's user reaches. It shows the place, its bots, their workspaces and their state, and signs in, registers and starts and stops bots. It holds no bot's token.
- **Service and network.** The daemon runs as a systemd or launchd service, opens no inbound port, and stops with the PC. A bot that must stay up belongs on a cluster or a naru host.

### Goal ④ widened

- **Goal ④ widens from a cluster to an execution place** (sauce, comment 19856107; garam-bot-operator#436). The cluster connector is goal ④'s as it stands. The naru-host connector is `garam-bot daemon` with the naru Machine provider, so goal ④ builds the daemon's core, and goal ⑤ adds what only a PC needs.
- **The issues, not filed, in order.** Letters are goal ④'s and numbers goal ⑤'s. An entry names the ones it waits for.
  - G1. garam: a place record for every kind of place (cluster, PC, host), widening garam ADR-0184's cluster record, and a registration token minted for a PC or a host.
  - G2. garam-bot: `api/cluster-connection/v1alpha2/`, the protocol widened to a place: its kind, one namespace and no claim for a PC or a host. After #1205 moves `v1alpha1`.
  - G3. garam: serve `v1alpha2` beside `v1alpha1`. Needs G1, G2.
  - G4. garam-bot: interface 3's naru Machine provider, passing the suite against naru's `machineapifake` and against orch. Needs garam-bot#1206 and naru#409.
  - G5. garam-bot: `garam-bot daemon`'s core: registration, key custody and rotation, the declaration feed, reports, one child `garam-bot run` per Bot, and the bot-token exchange. Needs G2 and garam#2049.
  - G6. garam-bot: the claude-code adapter of interface 2, in the workspace image. Needs garam-bot#1206.
  - G7. naru: the daemon as a naru host's service, with its registration token in the host's configuration and its caller registration (naru#409). Needs G5.
  - G8. End to end: a bot declared in garam runs on a naru host in a Machine, reached through naru-proxy, and a file-declared bot there shows in garam. Needs G3, G4, G5, G6, G7.
  - 5.1. garam-bot: the container provider (Docker and Podman on Linux, a container engine on a Mac) and the mvmd provider. Needs garam-bot#1206.
  - 5.2. garam-bot: the PC app's toolkit and packaging decided (an ADR), and `app/` scaffolded.
  - 5.3. garam-bot: the PC app's device sign-in and registration, the daemon's local socket, and the operating systems' key stores. Needs G5 and 5.2.
  - 5.4. garam-bot: the daemon as a systemd and a launchd service, with packages for Linux and macOS. Needs G5.
  - 5.5. garam-bot: the codex adapter of interface 2. Needs garam-bot#1206.
  - 5.6. End to end: a bot declared in garam runs on a Linux PC and on a Mac, shown in the PC app. Needs 5.1, 5.3, 5.4.

### What exists and what is to build

| Repository | Exists | To build |
|---|---|---|
| garam | Connected-cluster records (garam#2045) and their registration, `private_key_jwt` exchange and rotation (garam#2046), ADR-0184 to ADR-0186. OIDC with PKCE, the CLI's device grant (ADR-0120), the OIDC issuer (ADR-0141), Transit for message keys (ADR-0104) | Declarations long-polled (garam#2048, garam#2060 with ADR-0187). The cluster-connection server suite in CI (garam#2047). The bot-token mint as RFC 8693 (garam#2049). Reports (garam#2050). The bot's identity under IAM (garam#2027). garam declares and never calls naru (garam#2042). Place records (G1, G3). The broker (B1 to B6). Keys (garam#2061) |
| garam-bot-operator, moving to `operator/` (#1205) | The connector's registration, key custody and rotation (garam-bot-operator#449, ADR 0088, ADR 0093). Declarations applied with one owner per object (garam-bot-operator#452). Reports (garam-bot-operator#454). The cluster-connection interface and its reference server (ADR 0092, garam-bot-operator#447) | Conformance suites and ADR 0094's rotation period (garam-bot-operator#439). Bot tokens (garam-bot-operator#443). The Bot reconciler (garam-bot-operator#444). The chart (garam-bot-operator#445). End to end (garam-bot-operator#446). Registering again (garam-bot-operator#450). The cluster-connection interface to `api/` (#1205). The manager and the control service through the connector (garam-bot#1207) |
| garam-bot | Interface 1 (ADR 0032). The runtime that runs one Bot (ADR 0033). Interfaces 2 and 3 with reference implementations (garam-bot#1206, ADR 0034, ADR 0035) | The module rename and the operator's move (#1205). `cmd/sherlock` (ADR 0040). The naru Machine provider (G4). The daemon (G5). The adapters (G6, 5.5). The other providers (5.1). The PC app (5.2 to 5.4). Interfaces 4 `v1alpha2` (G2) and 5. Keys (garam-bot#1207) |
| naru | The Machine API's types, client, conformance test and in-memory implementation (`lib/machineapi`, ADR 0108), and the caller certificate (signer `POST /v1/caller`) | Orch serving the Machine API, placement, moves, and the proxy (goal naru#378). Callers registering themselves (naru#409). The daemon as a host service (G7). Keys (naru#412) |
| mvmd | One host's Machines behind the runtime API `v1alpha2` (gRPC on a Unix socket), with exec, files and disk snapshots | Nothing for this design beyond what naru#378 takes. The mvmd provider (5.1) is garam-bot's |
| codehub | A service principal in garam (garam `docs/architecture/machine-identity.md` §codehub's principal), AuthZEN with `private_key_jwt`, and a registry token issuer | Keys (codehub#327) |

### Auth inventory

Every issued PEM, hand-made certificate, hand-made key and signing key shared across purposes found in the seven code bases. Names only.

**Method.** At each repository's revision above, the following were read, and no secret was:
- **garam:** `deploy/charts/`, `deploy/local/`, and `docs/architecture/deployment-contract.md` and `machine-identity.md`;
- **garam-bot-operator:** `deploy/charts/` and `docs/architecture/configuration.md` and `connector.md`;
- **naru:** `docs/architecture/signer.md`, `orch.md` and `machines.md`;
- **codehub:** `docs/guide/deployment.md` and `docs/architecture/registry.md`;
- **aurora:** `docs/architecture/secrets.md` and `tls.md`;
- **garam-bot:** this repository's `deploy/local/` and `docs/architecture/`;
- **shell key generation:** `grep -rln "openssl genpkey\|openssl req\|openssl rand"` over every `*.sh` in each repository.

A key made in code from a template that names it in no document or script is outside what this method reaches.

| Repository | Item | Where | Kind | Target | Removed by |
|---|---|---|---|---|---|
| garam | key-encryption key, `GARAM_CA_KEY_FILE` | `deploy/charts/cluster/key-material.sh` makes it; aurora's `garam/kek/aurora` in AWS Secrets Manager | made by a shell script | Transit or KMS encryption | garam#2061, aurora#864 |
| garam | machine listener's TLS pair and `server-root.pem`, the root's key kept offline | `key-material.sh`, 365 days; aurora's `garam/{machine-tls,server-root}/aurora` | hand-made certificates | cert-manager | garam#2061, aurora#864 |
| garam | policy service's TLS pair, self-signed, and `garam-policy-ca` | `key-material.sh`; aurora's `garam/{policy-tls,policy-ca}/aurora` | hand-made certificate | cert-manager, OpenBao PKI | garam#2061, aurora#864 |
| garam | `GARAM_POLICY_SIGNING_KEY_FILE` and its `jwks.json` | `key-material.sh`; aurora's `garam/{signing-key,policy-jwks}/aurora` | shared across purposes (the policy feed and the OIDC issuer) and made by a shell script | a Transit key each, rotated, JWKS | garam#2061 |
| garam | message signing keys | sealed under the KEK in garam's database (ADR-0101); Transit is optional and off in aurora | key sealed under a hand-made KEK | Transit only | garam#2061, aurora#864 |
| garam | operator identities, an issued PEM each | garam ADR-0102, `operators.md` §Issued PEM identity | issued PEM | none: the connector's token (RFC 8693) | garam#2061, garam-bot#1207 |
| garam (cowork) | `COWORK_DEPLOYMENT_KEY_FILE` | aurora's `aurora/signing-key/cowork-deployment-key`, made and piped by hand | hand-made key | Transit, JWKS | garam#2061, aurora#864 |
| garam | `GARAM_IDENTITY_SERVICE_KEYS_FILE`, public keys | placed by hand | hand-placed trust | each service's JWKS URL | garam#2061 |
| garam | `deploy/local/{machine-material,policy-keys,policy-tls,cowork-keys,codehub-e2e-keys}.sh`, `deploy/local/minikube/{machine,policy}-key-material.sh` | `deploy/local/` | shell key generation | an OpenBao dev server and cert-manager, or material in memory | garam#2061 |
| garam | the earlier per-organization CA keys, sealed under the KEK | the `certificate_authorities` rows (ADR-0118) | hand-made keys | dropped | garam#1630 |
| garam | naru `caller` pair named `garam` | naru `docs/architecture/machines.md` §The first user: "garam holds a `caller` pair named `garam` in AWS Secrets Manager", replaced by naru#378's comment of 2026-11-24T12:39:34Z | issued certificate, by hand | none: garam does not call naru | naru#409, garam#2042 |
| garam-bot | the sherlock gateway's CA, server and client certificates (ADR 0012) | `deploy/local/README.md` §Certificates, `openssl` | hand-made certificates | none: `sherlock agent` is removed | ADR 0040, garam-bot#1207 |
| garam-bot | trusted issuers' `keys-ca-file`, pinning garam's server root | `internal/config/issuers.go` (ADR 0026) | pin of a hand-made root | none: removed with the gateway | ADR 0040, garam-bot#1207 |
| garam-bot | the bot token file `garam-bot run` reads | ADR 0033 | written by someone else | the connector's RFC 8693 exchange | garam-bot#1207, garam#2049 |
| garam-bot-operator | the manager's `--garam-private-key-file` and its client id, token URL and trust files | `docs/architecture/configuration.md`; aurora's `garam-agent-operator-system/garam-credential`, placed by hand | issued PEM | the connector's token | garam-bot#1207, aurora#864 |
| garam-bot-operator | the control service's `--operator-private-key-file` and `--garam-server-root-file` | `configuration.md` | issued PEM, pin of a hand-made root | the connector's token, cert-manager's CA | garam-bot#1207 |
| garam-bot-operator | the control service's serving pair, and `garam-agent-operator-control-root` | aurora `tls.md`: a five-year cert-manager root with `rotationPolicy: Never` | a root that never rotates | a rotating issuer | garam-bot#1207, aurora#864 |
| garam-bot-operator | the gaon chart's operator PEM, gaon client certificate and control root | `deploy/charts/garam-agent-operator-gaon/` | issued PEM, hand-made certificates | none: removed with the chart | garam-bot#1207 |
| naru | host, operator and orch authorities (ECDSA P-256, ten years) and the revocation key | files in the signer's directory (`signer.md`) | CA keys on disk | KMS signing | naru#412 |
| naru | root's person file and each operator's key | `naructl operator request`, `naructl operator person` | hand-made keys | KMS, or the reason recorded | naru#412 |
| naru | the recovery key | made outside the signer, kept by a person | root of trust | stays a person's (sauce-level, below) | — |
| naru | host certificates, a year with no renewal | `naructl signer rotate-host`, run by a person | renewed by hand | renewed by `naru-hostd` | naru#412 |
| naru | `caller` certificates, 90 days | `naructl caller credential` | issued by hand | registration and self-renewal | naru#409 |
| naru | `*.m.garam.sh` certificate | a file, by naru#378's coordinator decision of 2026-11-24 | hand-placed certificate | ACME | naru#412 |
| naru | enrollment keys, up to 365 days | `orch.md` §Enrollment | long-lived registration secret | one-time, a day at most | naru#412 |
| codehub | `CODEHUB_AUTHZEN_KEY_FILE` | aurora's `aurora/signing-key/codehub-authzen-key` | hand-made key | Transit, JWKS | codehub#327, aurora#864 |
| codehub | `CODEHUB_REGISTRY_KEY_FILE` and its derived JWKS | aurora's `aurora/signing-key/codehub-registry-signing` | hand-made signing key | Transit, JWKS, rotated | codehub#327, aurora#864 |
| codehub | `CODEHUB_AUTHZEN_CLIENT_SECRET`, the runner's `--client-secret-env` | `docs/guide/deployment.md` | shared secrets | `private_key_jwt`, registration | codehub#327 |
| aurora | `openbao-root`, five years, `rotationPolicy: Never` | `kubernetes/applications/openbao.yaml` | a root that never rotates | rotating, or the reason recorded | aurora#864 |
| aurora | RustFS's pairs `rustfs/user/root`, `rustfs/user/codehub`, `rustfs/user/qnt-github-actions` | OpenBao KV, each pair made by a person (aurora `secrets.md` §RustFS's credentials) | hand-made credentials | generated in place and rotated | aurora#864 |
| aurora | the Transit mount and key in `platform/openbao/garam-machine.tf` | declared, used by nothing | unused | used by garam | aurora#864 |
| aurora | OpenBao's unseal and recovery output | sauce's USB, `clusters/aurora/openbao-init.txt` | root of trust | stays a person's (sauce-level, below) | — |
| aurora | `github/token/aurora-argocd`, `proxmox/api-token/infra`, `cloudflare/token/aurora-cert-manager`, `minimax/api-key/garam`, `google/client/garam-console-dev` | AWS Secrets Manager and OpenBao | external service tokens | stay, handled by people | — |

- **Sauce-level changes, listed and not filed:**
  - the KMS root's key policy, which names who may sign and decrypt with it;
  - naru's recovery key and OpenBao's unseal output stay with sauce as the recovery path, and moving either into KMS is sauce's decision;
  - registering garam's official app with GitHub, and later with each other service (B8).

## Rationale

- ADR 0031: the bot is the unit, an agent is its harness, and garam is optional.
- ADR 0032: interface 1, one schema for a file and a CRD.
- ADR 0034: why the workspace provider is a Go driver interface, and why a naru Machine is its first production implementation.
- ADR 0035: why the agent interface is an HTTP protocol through `Dial`, and why tools are MCP servers outside the workspace.
- ADR 0036: why a bot is declared anywhere and managed in garam, why one connector per place, and why only pull.
- ADR 0037: why OAuth and OIDC standards, why one signing key per purpose, and why no key is made by hand.
- ADR 0038: why garam brokers external apps through one official app per service, with scopes in IAM.
- ADR 0039: why one daemon is the connector of a PC and a naru host, and why the PC app only shows and drives it.
- ADR 0040: why `cmd/sherlock` goes, and what moves under `garam-bot`.
- garam-bot-operator ADR 0087 to ADR 0094 and garam ADR-0184 to ADR-0187: the cluster's connector and registration, which this design widens to every place.

## Open questions

- **The place record's GRN.** Whether garam widens `grn:<org>/iam/cluster/<id>` with a kind or takes a new type for every place is garam's, in G1.
- **Messages to a bot.** Whether a bot's process long-polls garam's mailbox itself or its connector carries messages for every bot it runs is not decided. Either is pull. The first is simpler; the second keeps one connection per place.
- **Interface 5.** The model gateway's specification, and where it runs at each place, are not written.
- **The bot's memory.** Whether the memory store ADR 0040 keeps becomes the bot-memory interface, and where it lives at each place, is not decided.
- **Density.** How many bots and workspaces a PC, a host and a cluster pod run is not measured (interface 2's open questions).
- **Naru's recovery path in KMS.** Whether root's person file and the recovery key can become KMS keys is naru#412's to find, and sauce's to decide.
