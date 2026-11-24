# Bot system

The whole bot system across its repositories:
- where a bot is declared and where it runs;
- how each execution place connects to garam, and how every party authenticates;
- where keys live, and how workspaces, agents and external apps are reached;
- the repository layout, and what exists and what is to build, per repository.

This file governs the shape that crosses repositories. Each interface's own specification is under `api/`, and each repository's own documents govern its inside.

Every claim about another repository below holds at the revision it names:
- garam `dc030cd1c48b8324b207a65a2606448ecb9203ea`;
- garam-bot-operator `b9c37548fcd303a6bd51e2bb5795aa195c779356`;
- naru `08ec2045056c4a14049fe7fcbbec41748136c648`;
- mvmd `196b7a455e4d6a50c2333c718d8333f4e3f94739`;
- codehub `d738c411d7d6720b3801516cfc9475163cd1c728`;
- aurora `2de277ad451ea8c543801b440096124c09603c9f`.

## Current decisions

### Declare anywhere, manage in garam

- **One schema, three places to declare** (ADR 0036). Interface 1, `bot.garam.sh` at `v1alpha1`, reads the same from a file and a CRD (ADR 0032). A bot is declared:

  | Where | Owner | Marker |
  |---|---|---|
  | garam's dashboard or API, placed on one execution place | garam | `bot.garam.sh/managed-by: garam`, the place's GRN and the declaration's identifier and generation |
  | a CRD in a cluster | the cluster | none, or `cluster` |
  | a file at a PC or a naru-host place | that place | none, or `cluster` |

  Interface 1's OWN-1 calls an object marked `cluster` "the cluster's". It is to name the place's own objects at every kind of place, by a wording release of interface 1 (§Goals ④ and ⑤, G7).
- **garam shows every bot of every place** from the place's reports, and changes only what it owns. A place applies garam's declarations and never changes its own objects on garam's word (the cluster-connection protocol's DECL-11 to DECL-14).
- **A bot runs without garam** (ADR 0031). A place that loses garam keeps running what it runs, and a bot declared at its place needs no garam.
- **Only a bot garam declared and placed has a garam identity.** Its connector obtains its token (§Authentication). A bot declared at its place runs with no garam token: garam ADR-0184's decision "A cluster's token reaches the cluster-connection routes, and the mint of the tokens of a bot placed on that cluster by a live declaration, and nothing else" and the protocol's AUTH-5 refuse one. Whether such a bot gets a garam identity is open with garam (§Open questions).

### Execution places and their connectors

- **An execution place is a cluster, a PC or a naru-host place, and each runs one connector, registered once** (ADR 0036, ADR 0039).

  | Place | Connector | Installed by | A bot's process runs as | Workspace providers (interface 3) |
  |---|---|---|---|---|
  | Kubernetes cluster | the connector in `operator/`, beside its manager (garam-bot-operator ADR 0090) | its Helm chart | one StatefulSet of one, the runtime image (ADR 0033) | Pod; naru Machine where the cluster reaches a naru fleet |
  | Linux PC | `garam-bot daemon` | a system package, a systemd service | a child `garam-bot run` | container, and mvmd with KVM |
  | Mac | `garam-bot daemon` | a package, a launchd service | a child `garam-bot run` | container |
  | naru-host place | `garam-bot daemon`, on a machine beside the fleet | its own package | a child `garam-bot run` | naru Machine |

- **A naru-host place is a use of naru, not a part of it.** naru ADR 0108's decision "Three layers, each calling only the one below" places a use above naru, calling the Machine API alone. The daemon runs beside the fleet and calls orch's Machine API as a caller (naru#409). It is not a service of a naru host, and naru's host networking drops a Machine's connections to its host.
- **Registration, the same at every place:**
  - a one-time registration token, minted in garam for the place, valid one hour, spent on its first use, with only its hash kept (garam ADR-0184);
  - a key the connector makes, RSA 3072 or more, which never leaves the place: a Secret in a cluster, the operating system's key store or an owner-only file on a PC or a naru-host place;
  - the connector rotates it itself, every 30 days by default, a period its `--rotation-period` sets (operator ADR 0094, `operator/docs/architecture/connector.md`);
  - revoking a key or deleting the place in garam refuses its next request.
- **After registration, garam chooses which bot runs where.** A bot declared in garam is placed on one place, and that place's connector takes it in its declaration feed and runs it.
- **One protocol for every place.** Interface 4 is the cluster-connection protocol, `v1alpha1` today (`api/cluster-connection/v1alpha1/`, moved from garam-bot-operator by garam-bot#1205). It widens to every place by a new version carrying the place's kind. A PC or a naru-host place holds one namespace and no namespace claim.

### Pull only

- **Every connection is opened by the place** (ADR 0036). garam opens no connection to a place, and a place opens no inbound port (garam-bot-operator ADR 0087, ADR 0091; the protocol's T-1).
- **Declarations** are long-polled with a cursor, tombstones and a purge point (the protocol's DECL-1 to DECL-6; garam ADR-0187).
- **Reports** are pushed by the place, as its whole view, within 10 seconds of a change and every 5 minutes (the protocol's REP-1 to REP-9). garam keeps the latest as untrusted display data (garam ADR-0188).
- **Messages to a bot** reach its process by long poll or stream that the bot's process opens with its token. garam's mailbox long poll for a bot is garam's to build (G4).
- **naru** is reached the same way. garam declares and never calls naru's Machine API (garam#2042). The Machine API's caller is garam-bot's naru Machine provider, at the place (naru#409, naru#378).

### Authentication

The standards are ADR 0037's.

| Party | Standard | Token | What exists | What is to build |
|---|---|---|---|---|
| A person in the console | OIDC authorization code with PKCE | garam's session | garam `docs/architecture/sign-in.md`, Google as provider | — |
| The CLI | RFC 8628 device authorization | a session held as a bearer | garam ADR-0120 | — |
| The PC app | RFC 8628 device authorization | a session held as a bearer | — | goal ⑤, on the CLI's endpoints |
| A connector | `private_key_jwt` (RFC 7523) | one hour, read on every request | garam ADR-0184, garam#2046 | the PC and naru-host place records (§Goals ④ and ⑤, 5.1) |
| A bot garam declared and placed | RFC 8693 token exchange by its place's connector, bound to the bot process's key by DPoP (RFC 9449), below | at most one hour, DPoP-bound, held by the bot's process, never in a workspace | — | the exchange, garam#2049 (PR garam#2077, open); the binding, garam#2078; garam-bot#1207 |
| A bot declared at its place | none from garam | — | — | open with garam |
| A tool or a model | garam's model and tool tokens, DPoP-bound to the bot process's key; in the workspace, tokens the place's gateway and tool proxy issue for their own use | at most one hour; the workspace's in token files (interface 2's CRED-1) | — | interface 5 (G3); the tool proxy (G6, B6); garam#2078 |
| A first-party service (cowork, codehub) | `private_key_jwt` with a key in Transit | at most 300 seconds, for the policy service | garam ADR-0129, `docs/architecture/machine-identity.md` §cowork's principal | garam#2061, codehub#327 |
| A naru caller | a certificate the signer issues, renewed by its holder | 90 days at most | `naructl caller credential`, by hand | naru#409 |

- **The bot-token exchange** (ADR 0037), for garam#2049 to specify:
  - the connector calls garam's token endpoint with `grant_type` `urn:ietf:params:oauth:grant-type:token-exchange`;
  - `actor_token` is the connector's own access token;
  - `subject_token` is of a garam-defined token type whose value names the bot's GRN;
  - `audience` is garam's, and `scope` lists what the bot may call, with nothing implied;
  - the issued token's `sub` is the bot's GRN, `act.sub` the place's GRN, `aud` garam's audience, `scope` what was asked and allowed, and it expires within an hour;
  - garam refuses a bot that no live declaration places on that place;
  - the request carries a DPoP proof the bot's process signed, and the issued token is bound to that key (below).
- **Every token garam issues to a bot's process is bound to that process's key** (sauce, `garamsh/discussions` discussion 6, comment 19863377; ADR 0037; garam#2078, under garam ADR-0191, which is reserved and not merged):
  - **What is bound:** the access token the exchange issues, and the model and tool tokens garam issues to bots (the ai-gateway's, and the brokered apps' of §External apps). Each is a DPoP token (RFC 9449), `token_type` `DPoP`, carrying the key's thumbprint as `cnf.jkt`. A token garam does not issue, such as a `secretRef` tool's credential, is outside this.
  - **The key** is made in the bot's process when it starts, and never leaves it: not to a file, a Secret, the connector or a workspace. A new process makes a new key and needs a new exchange, so a token bound to an earlier process's key is never used again.
  - **Every request** carries a proof signed by that key, with `htm`, `htu`, `iat`, `jti`, and `ath` where a token is sent. garam's resource servers verify the proof and `cnf.jkt`, refuse a `jti` seen within the `iat` window, and refuse a bound token presented as `Bearer`. The token endpoint supports `DPoP-Nonce`.
  - **The exchange's proof is the bot's process's.** The connector sends a proof the bot's process signed for the token endpoint, beside its own token as the `actor_token`, so the connector never holds the bot's key. How a proof passes from the bot's process to its connector at each kind of place is garam-bot#1207's to build (§Open questions).
  - **Not mTLS binding (RFC 8705):** a place does not control where TLS is terminated between it and garam, and a DPoP proof passes any proxy.
  - **No bound token enters a workspace,** because the harness cannot sign with the bot process's key. A workspace's token files hold tokens the place's model gateway and tool proxy issue for their own use (interface 2's CRED-1), and garam's bound tokens are presented outside it, with proofs from the bot's process.
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
  - naru's signer for naru's own parties, renewed by the holder;
  - ACME for a public name: naru's `*.m.garam.sh` by DNS-01 (§Key management).

### Key management

- **The target** (sauce, `garamsh/discussions` discussion 6, comment 19857399; ADR 0037):
  - **Nothing made by hand.** No key is made, placed or renewed by a person. The root of trust is AWS KMS, which holds no value. People handle it and external service tokens (GitHub, Proxmox, Cloudflare), and nothing else.
  - **Recovery keys are kept offline** (comment 19858505). OpenBao's recovery keys and naru's recovery key are emergency keys the system makes once and uses only in a disaster. They stay on USB, offline, and do not move to KMS. Only day-to-day operating keys are in KMS and Transit.
  - **KEKs and signing keys** are held in OpenBao Transit or KMS, and never leave it.
  - **Internal TLS and CAs** come from cert-manager, or from OpenBao PKI where needed.
  - **The OIDC issuer** has its own key, rotates it, and publishes JWKS.
  - **The operator, the connector and the PC daemon** register with a token and a key made on site (garam-bot-operator ADR 0088).
  - **Shell key generation goes**, `deploy/charts/cluster/key-material.sh` first.
- **naru's `*.m.garam.sh`** is issued and renewed by ACME DNS-01 (comment 19858505). naru is given a Cloudflare token that can only edit DNS in the `m.garam.sh` zone, named for the service that consumes it. sauce issues it in the Cloudflare dashboard at the registration step at the end of the build. Until then the certificate stays the hand-placed file.
- **Ownership.** The code side is the PM's: garam, garam-bot and naru, naru's signer CA included (comment 19857399), and codehub. Ian owns aurora's OpenBao Transit, PKI and cert-manager setup.
- **The issues, in the order they land.** Each lands behind configuration that keeps today's path, so current build work is not blocked, and all finish by the registration step at the end of the build:
  1. garam#2061: Transit for every garam signing key and the KEK, the issuer's own key, cert-manager TLS, no operator PEM, no key scripts.
  2. naru#412, beside codehub#327:
     - naru#412: the signer's authorities in KMS, certificates renewed by their holders, ACME DNS-01 for the proxy;
     - codehub#327: the AuthZEN and registry keys in Transit, no shared secrets.
  3. garam-bot#1207: the manager and the control service through the connector, bot tokens by RFC 8693, no gateway TLS.
  4. aurora#864. aurora ADR 0037 (aurora#862) built ahead of the switch:
     - the Transit keys `garam-kek`, `garam-issuer`, `garam-policy-feed`, `garam-message`, `codehub-authzen` and `cowork-deployment`;
     - a PKI root with the intermediates `pki_garam_machine` and `pki_int`;
     - the issuers `openbao-internal` and `openbao-garam-machine`;
     - trust-manager's bundle `aurora-internal-ca`.

     Nothing reads them yet. What remains:
     - a Transit key for codehub's registry tokens, which aurora ADR 0037 does not declare;
     - the Transit roles from the PM's map of ServiceAccount to key;
     - the switch, at the registration step: garam's switch release, aurora connected by the connector's registration token, and every hand-placed key deleted.

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
  | naru-host place | naru Machine |

- **The agent interface** (ADR 0035) is the HTTP protocol `agent/v1alpha1`, `api/agent/v1alpha1/` (garam-bot#1206):
  - an adapter in the workspace image serves it on port 7700, and the bot's process reaches it through `Dial`;
  - a session runs one harness: Claude Code, Codex or another adapter;
  - tasks carry the bot's mailbox, and events are long-polled. The bot's process keeps the bot's memory from them.
- **No long-lived secret enters a workspace** (interface 2's CRED-1). Its credentials are the adapter's token, the model gateway's token and the tool tokens. Each lasts at most an hour, and the bot's process rewrites it.
- **Every tool is an MCP server outside the workspace.** The bot's process, or its tool proxy at the place, adds the tool's own credential in one of three modes:
  - `garam`: §External apps, for a bot garam declared;
  - `secretRef`: a Secret the declaration names, read at the place;
  - `federated`: a token exchanged at the tool's issuer.
- **Model calls go to interface 5**, the model gateway, with a token file the bot's process keeps current. Interface 5 is not written yet. Calls are billed by API through the gateway only, with no subscription login, and an organization may bring its own provider key, which the gateway holds (comment 19858505).

### External apps

- **garam brokers external apps** (ADR 0038; sauce, comment 19857097):
  - **one official app per external service,** GitHub first, installed once per organization; garam records each installation under the organization;
  - **the scope lives in garam IAM:** a grant names which bot may use which installation with which scope, and garam's policy service decides each request as an AuthZEN evaluation. There is no Connection resource (comment 19791501);
  - **short narrowed tokens:** a bot's tool proxy at the place exchanges the bot's token by RFC 8693 for one installation and one scope, and garam mints the service's narrowest token, at most an hour long. For GitHub that is an installation token with `repositories` and `permissions` set. garam calls the service, never the place;
  - **a person's OAuth** is used only when a task acts for that person, and only on that person's grant (garam#1787 decides how a token carries who a bot acts for);
  - **an organization may bring its own app,** under the same grants and exchange;
  - **the apps' private keys are held in Transit,** and sign the apps' JWTs there.

  A bot declared at its place has no garam token, so it reaches tools by `secretRef` or `federated` only.
- **The issues it needs, not filed, in order** (garam's unless named):
  - B1. sauce-level: registering garam's official GitHub App under the `garamsh` organization, and later each other service's app. Creating an app and its key is a person's act at the service, once. sauce decides it when goal ⑤ starts (comment 19858505).
  - B2. The apps' private keys in Transit, signing the app JWTs there. Needs B1 and garam#2061.
  - B3. Installations: an organization installs garam's official GitHub App, and garam records the installation under the organization, with console pages. Needs B1.
  - B4. The grants: IAM actions and a Cedar policy shape for "this bot may use this installation with this scope", evaluated through AuthZEN. Needs B3.
  - B5. The external token exchange: RFC 8693 at garam's token endpoint, a bot's token in, a GitHub installation token narrowed by `repositories` and `permissions` out. Needs B2, B3, B4 and garam#2049.
  - B6. garam-bot: the tool proxy's `garam` mode, which exchanges and caches each token within its life and adds it to the MCP server's calls. Needs B5 and G6. It supersedes garam-bot#840.
  - B7. A person's grant on that person's behalf: GitHub user-to-server tokens, sealed in Transit and refreshed by garam. Needs B5 and garam#1787.
  - B8. An organization's own app, registered with its key placed into Transit. Needs B5.

### Repository layout

- **One repository per product** (sauce, comment 19857166; garam-bot#1205). What changes and releases together is one repository. What differs in access, public or private, or is installed by others on its own, is split. Each component inside has its own Go module and version tags, so it can be published or split off alone. Eventually all code is managed in codehub.

  | Repository | Holds |
  |---|---|
  | garam-bot | the runtime and `garam-bot daemon` (root module `github.com/garamsh/garam-bot`), the interfaces (`api/`, tags `api/vX.Y.Z`), the cluster connector and manager (`operator/`, tags `operator/vX.Y.Z`, moved from garam-bot-operator by #1205), and the PC app (`app/`) |
  | garam | the platform server, the console and cowork, pinning a released `garam-bot/api` |
  | codehub | unchanged |
  | naru, mvmd | separate for now; the PM decides whether to join them once naru's goal ③ design settles |
  | aurora | unchanged, private |
  | skills, convention-driven-project | unchanged: general-purpose, installed by others |
  | the products (qnt, knowno, neocity, murim-24, janggi12) | unchanged |

- **garam-bot is not merged into garam** (comment 19857166):
  - a bot must run without garam (ADR 0031), and the boundary is crossed only through versioned public interfaces;
  - garam is deployed to one cluster of ours, while garam-bot is installed in users' clusters and PCs, so it needs its own compatibility promise;
  - kept apart, it is easier to publish as open source.

  garam pins a released version of `garam-bot/api`, and a breaking change only comes with a new version.
- **`cmd/sherlock` goes** (ADR 0040):
  - `sherlock agent` and the in-house loop are removed;
  - the workspace server, its operations and its MCP server move under `garam-bot workspace`;
  - the memory store stays as the candidate for the bot's memory.

  It follows #1205's module rename (G8).

### The PC daemon and the PC app

These are designed in goal ④ and built in goal ⑤ (sauce, comment 19857097; ADR 0039).

- **Ownership.** garam-bot owns both: `garam-bot daemon` in the root module, the PC app in `app/`.
- **Registration:**
  1. A person signs in to the PC app with the device grant.
  2. The app asks garam for a one-time token for a new PC place in the person's organization, and hands it to the daemon over the local socket.
  3. The daemon makes its key in the operating system's key store, or in an owner-only file, and registers.

  The daemon rotates its key itself, and the person never sees it.
- **Running several bots.** The daemon long-polls the declarations placed on its place and reads the place's own from a directory of files. For each Bot it runs one child `garam-bot run`, with workspaces from its providers:
  - container (Docker or Podman), or mvmd where the PC has KVM, on Linux;
  - container on a Mac.

  A Bot garam declared and placed runs with the token the daemon exchanges (RFC 8693) and rewrites. A Bot declared in a file runs with no garam token. The daemon reports the place's objects to garam as a cluster connector does.
- **The PC app** reads the daemon over a local socket only the PC's user reaches. It shows the place, its bots, their workspaces and their state, and signs in, registers and starts and stops bots. It holds no bot's token.
- **Service and network.** The daemon runs as a systemd or launchd service, opens no inbound port, and stops with the PC. A bot that must stay up belongs on a cluster or a naru-host place.
- **Costs left open.** A notarized macOS package needs an Apple Developer Program membership. sauce decides it when goal ⑤ starts (comment 19858505).

### Goals ④ and ⑤

- **Goal ④ widens from a cluster to an execution place in the design, and builds the cluster** (sauce, comments 19856107 and 19857097; garam-bot-operator#436). Goal ④ builds no daemon core. In its first version the naru Machine provider runs inside the cluster connector on aurora, so goal ④ does not wait on a naru-host place. The PC, the naru-host place and the daemon are built in goal ⑤.
- **The issues, not filed, in order.** Letters are goal ④'s and numbers goal ⑤'s. An entry names the ones it waits for. Goal ④'s cluster connector issues are filed already: garam-bot-operator#439, #443, #444, #445, #446 and #450.
  - G1. garam-bot: interface 3's naru Machine provider, run inside the cluster connector, passing the suite against naru's `machineapifake` and against orch. Needs garam-bot#1206, naru#409, and naru#378's orch serving the Machine API with Machines placed and naru-proxy, through which `Dial` reaches a Machine.
  - G2. garam-bot: the claude-code adapter of interface 2, in the workspace image. Needs garam-bot#1206 and the check of Claude Code's terms that interface 2's open questions name.
  - G3. garam-bot and garam: interface 5 `v1alpha1` and a model gateway at the place, billed by API with BYOK. Needs garam#2027's ai-gateway record.
  - G4. garam: messages to a bot, by a long poll the bot's process opens with its token. Needs garam#2049.
  - G5. garam-bot: interface 3's Pod provider for a cluster. Needs garam-bot#1206.
  - G6. garam-bot: the tool proxy at the place, in the `secretRef` and `federated` modes. Needs garam-bot#1206.
  - G7. garam-bot: a wording release of interface 1, in which OWN-1's `cluster` names the place's own objects at every kind of place.
  - G8. garam-bot: ADR 0040's removal of `sherlock agent` and the move of `sherlock workspace` under `garam-bot`. Needs garam-bot#1205.
  - G9. garam-bot: the callers' suites of interfaces 2 and 3, their pending cases (CRED-1, VER-2) included. Needs G1 and G2.
  - G10. End to end on aurora: a bot declared in garam runs through the cluster connector in a naru Machine, reached through naru-proxy, with the claude-code adapter, the gateway, its messages and a tool by `secretRef`, and a cluster-declared bot shows in garam. Needs G1 to G6, garam-bot-operator#443 to #446, and garam#2049.
  - 5.1. garam: a place record for every kind of place (cluster, PC, naru-host place), widening garam ADR-0184's cluster record, and a registration token minted for a PC or a naru-host place.
  - 5.2. garam-bot: `api/cluster-connection/v1alpha2/`, the protocol widened to a place: its kind, one namespace and no claim for a PC or a naru-host place, with its reference server. After #1205 moves `v1alpha1`.
  - 5.3. garam: serve `v1alpha2` beside `v1alpha1`. Needs 5.1 and 5.2.
  - 5.4. garam-bot: `garam-bot daemon`'s core: registration, key custody and rotation, the declaration feed, reports, one child `garam-bot run` per Bot, and the bot-token exchange for a Bot garam placed. It passes the client suite against 5.2's reference server, then runs against 5.3. Needs 5.1, 5.2 and garam#2049.
  - 5.5. garam-bot: the naru-host place: the daemon beside the fleet with the naru Machine provider, as a naru caller (naru#409). Needs 5.4 and G1.
  - 5.6. garam-bot: the container provider (Docker and Podman on Linux, a container engine on a Mac) and the mvmd provider. Needs garam-bot#1206.
  - 5.7. garam-bot: the PC app's toolkit and packaging decided (an ADR), and `app/` scaffolded.
  - 5.8. garam-bot: the PC app's device sign-in and registration, the daemon's local socket, and the operating systems' key stores. Needs 5.4 and 5.7.
  - 5.9. garam-bot: the daemon as a systemd and a launchd service, with packages for Linux and macOS. Needs 5.4, and sauce's decision on the Apple Developer membership.
  - 5.10. garam-bot: the codex adapter of interface 2. Needs garam-bot#1206 and the check of Codex's terms.
  - 5.11. End to end: a bot declared in garam runs on a naru-host place, on a Linux PC and on a Mac, shown in the PC app. Needs 5.3, 5.5, 5.6, 5.8 and 5.9.

### What exists and what is to build

| Repository | Exists | To build |
|---|---|---|
| garam | Connected-cluster records (garam#2045) and their registration, `private_key_jwt` exchange and rotation (garam#2046), ADR-0184 to ADR-0186. Declarations long-polled (garam#2048, ADR-0187). Reports kept as untrusted display data (garam#2050, ADR-0188). OIDC with PKCE, the CLI's device grant (ADR-0120), the OIDC issuer (ADR-0141), Transit for message keys (ADR-0104) | The cluster-connection server suite in CI (garam#2047). The bot-token mint as RFC 8693 (garam#2049, PR garam#2077). Every token it issues to a bot's process bound by DPoP (garam#2078, garam ADR-0191 reserved). The bot's identity under IAM (garam#2027). garam declares and never calls naru (garam#2042). The vendored cluster-connection specification re-pinned to `api/cluster-connection/v1alpha1/` after #1205, deciding there whether ADR-0186 needs a successor (#1205's consumers). Messages to a bot (G4). Place records (5.1, 5.3). The broker (B2 to B5, B7, B8). Keys (garam#2061) |
| garam-bot's `operator/`, moved from garam-bot-operator (#1205) | The connector's registration, key custody and rotation (garam-bot-operator#449, ADR 0088, ADR 0093). Declarations applied with one owner per object (garam-bot-operator#452). Reports (garam-bot-operator#454). The cluster-connection interface and its reference server (ADR 0092, garam-bot-operator#447), now in `api/` (#1205). Conformance suites and the rotation period as a flag (garam-bot-operator#439, ADR 0094) | Bot tokens (garam-bot-operator#443). The Bot reconciler (garam-bot-operator#444). The chart (garam-bot-operator#445). End to end (garam-bot-operator#446). Registering again (garam-bot-operator#450). The manager and the control service through the connector (garam-bot#1207) |
| garam-bot | Interface 1 (ADR 0032). The runtime that runs one Bot (ADR 0033). Interfaces 2 and 3 with reference implementations (garam-bot#1206, ADR 0034, ADR 0035). The module rename and the operator's move (#1205) | `cmd/sherlock` (G8). The naru Machine provider (G1) and the Pod provider (G5). The adapters (G2, 5.10). Interface 5 and the gateway (G3). The tool proxy (G6, B6). Interface 1's wording release (G7). The callers' suites (G9). Interface 4 `v1alpha2` (5.2). The daemon (5.4) and the naru-host place (5.5). The other providers (5.6). The PC app (5.7 to 5.9). Keys (garam-bot#1207) |
| naru | The Machine API's types, client, conformance test and in-memory implementation (`lib/machineapi`, ADR 0108). orch serving the Machine API on its listener, with its records and each caller's event log, and no Machine placed (naru#381). The caller certificate (signer `POST /v1/caller`) | Placement, moves, exec and files, and the proxy (goal naru#378). Callers registering themselves (naru#409), with `machines.md` §The first user, which still names garam as the caller, rewritten. Keys (naru#412) |
| mvmd | One host's Machines behind the runtime API `v1alpha2` (gRPC on a Unix socket), with exec, files and disk snapshots | Nothing for this design beyond what naru#378 takes. The mvmd provider (5.6) is garam-bot's |
| codehub | A service principal in garam (garam `docs/architecture/machine-identity.md` §codehub's principal), AuthZEN with `private_key_jwt`, and a registry token issuer | Keys (codehub#327) |
| aurora | Transit keys, PKI, cert-manager issuers and a trust bundle built ahead of the switch (aurora ADR 0037, aurora#862) | The registry's Transit key, the Transit roles, and the switch (aurora#864) |

### Auth inventory

Every issued PEM, hand-made certificate, hand-made key and signing key shared across purposes found in the seven code bases. Names only.

**Method.** At each repository's revision above, the following were read, and no secret was:
- **garam:** `deploy/charts/`, `deploy/local/`, and `docs/architecture/deployment-contract.md` and `machine-identity.md`;
- **garam-bot-operator:** `deploy/charts/` and `docs/architecture/configuration.md` and `connector.md`;
- **naru:** `docs/architecture/signer.md`, `orch.md`, `machines.md` and `proxy.md`;
- **codehub:** `docs/guide/deployment.md` and `docs/architecture/registry.md`;
- **aurora:** `docs/architecture/secrets.md` and `tls.md`, `platform/openbao/`, and its ADR 0037;
- **garam-bot:** this repository's `deploy/local/` and `docs/architecture/`;
- **shell key generation:** `grep -rln "openssl genpkey\|openssl req\|openssl rand"` over every `*.sh` in each repository, and in this repository `openssl x509` as well. Every script it matched has a row.

A key made in code from a template that names it in no document or script is outside what this method reaches.

| Repository | Item | Where | Kind | Target | Removed by |
|---|---|---|---|---|---|
| garam | key-encryption key, `GARAM_CA_KEY_FILE` | `deploy/charts/cluster/key-material.sh` makes it; aurora's `garam/kek/aurora` in AWS Secrets Manager | made by a shell script | Transit `garam-kek` | garam#2061, aurora#864 |
| garam | machine listener's TLS pair and `server-root.pem`, the root's key kept offline | `key-material.sh`, 365 days; aurora's `garam/{machine-tls,server-root}/aurora` | hand-made certificates | cert-manager's `openbao-garam-machine` under `pki_garam_machine` | garam#2061, aurora#864 |
| garam | policy service's TLS pair, self-signed, and `garam-policy-ca` | `key-material.sh`; aurora's `garam/{policy-tls,policy-ca}/aurora` | hand-made certificate | cert-manager's `openbao-internal` under `pki_int` | garam#2061, aurora#864 |
| garam | `GARAM_POLICY_SIGNING_KEY_FILE` and its `jwks.json` | `key-material.sh`; aurora's `garam/{signing-key,policy-jwks}/aurora` | shared across purposes (the policy feed and the OIDC issuer) and made by a shell script | Transit `garam-policy-feed` and `garam-issuer`, rotated, JWKS | garam#2061 |
| garam | message signing keys | sealed under the KEK in garam's database (ADR-0101); Transit is optional and off in aurora | key sealed under a hand-made KEK | Transit `garam-message` only | garam#2061, aurora#864 |
| garam | operator identities, an issued PEM each | garam ADR-0102, `operators.md` §Issued PEM identity | issued PEM | none: the connector's token (RFC 8693) | garam#2061, garam-bot#1207 |
| garam (cowork) | `COWORK_DEPLOYMENT_KEY_FILE` | aurora's `aurora/signing-key/cowork-deployment-key`, made and piped by hand | hand-made key | Transit `cowork-deployment`, JWKS | garam#2061, aurora#864 |
| garam | `GARAM_IDENTITY_SERVICE_KEYS_FILE`, public keys | placed by hand; aurora's committed `garam-service-keys` | hand-placed trust | each service's JWKS URL | garam#2061, aurora#864 |
| garam | `deploy/local/{machine-material,policy-keys,policy-tls,cowork-keys,codehub-e2e-keys}.sh`, `deploy/local/minikube/{machine,policy}-key-material.sh` | `deploy/local/` | shell key generation | an OpenBao dev server and cert-manager, or material in memory | garam#2061 |
| garam | `tooling/codehub-e2e.sh`, `tooling/cowork-image-e2e.sh` | `tooling/` | shell key generation in test fixtures | material in memory | garam#2061 |
| garam | the earlier per-organization CA keys, sealed under the KEK | the `certificate_authorities` rows (ADR-0118) | hand-made keys | dropped | garam#1630 |
| garam | naru `caller` pair named `garam` | naru `docs/architecture/machines.md` §The first user: "garam holds a `caller` pair named `garam` in AWS Secrets Manager", replaced by naru#378's comment of 2026-11-24T12:39:34Z | issued certificate, by hand | none: garam does not call naru | naru#409, garam#2042 |
| garam-bot | the sherlock gateway's CA, server and client certificates (ADR 0012) | `deploy/local/README.md` §Certificates, `openssl` | hand-made certificates | none: `sherlock agent` is removed | ADR 0040, garam-bot#1207 |
| garam-bot | trusted issuers' `keys-ca-file`, pinning garam's server root | `internal/config/issuers.go` (ADR 0026) | pin of a hand-made root | none: removed with the gateway | ADR 0040, garam-bot#1207 |
| garam-bot | `deploy/local/smoke.sh`, by `openssl req` and `openssl x509 -CA` | `deploy/local/` | shell key generation in a test fixture | material in memory, or none once the gateway goes | garam-bot#1207 |
| garam-bot | the bot token file `garam-bot run` reads | ADR 0033 | written by someone else | the connector's RFC 8693 exchange, DPoP-bound to a key the bot's process makes | garam-bot#1207, garam#2049, garam#2078 |
| garam-bot-operator | the connector's `--garam-root-file`, the root garam's certificate chains to (T-2) | `configuration.md`; ADR 0088's decision "Registration is one call that spends a one-time token" verifies garam against "the server root the chart was given" | hand-placed trust root | the `aurora-internal-ca` bundle, or a public CA | garam-bot#1207, aurora#864 |
| garam-bot-operator | the manager's `--garam-private-key-file` and its client id, token URL and trust files | `docs/architecture/configuration.md`; aurora's `garam-agent-operator-system/garam-credential`, placed by hand | issued PEM | the connector's token | garam-bot#1207, aurora#864 |
| garam-bot-operator | the control service's `--operator-private-key-file` and `--garam-server-root-file` | `configuration.md` | issued PEM, pin of a hand-made root | the connector's token, cert-manager's CA | garam-bot#1207 |
| garam-bot-operator | the control service's serving pair, and `garam-agent-operator-control-root` | aurora `tls.md`: a five-year cert-manager root with `rotationPolicy: Never` | a root that never rotates | a rotating issuer | garam-bot#1207, aurora#864 |
| garam-bot-operator | the gaon chart's operator PEM, gaon client certificate and control root | `deploy/charts/garam-agent-operator-gaon/` | issued PEM, hand-made certificates | none: removed with the chart | garam-bot#1207 |
| garam-bot-operator | `tests/gaon/guest-image.sh`, `tests/pairrun/pair-run.sh` | `tests/` | shell key generation in test fixtures | material in memory | garam-bot#1207 |
| naru | host, operator and orch authorities (ECDSA P-256, ten years) and the revocation key | files in the signer's directory (`signer.md`) | CA keys on disk | KMS signing | naru#412 |
| naru | root's person file and each operator's key | `naructl operator request`, `naructl operator person` | hand-made keys | KMS, or the reason recorded | naru#412 |
| naru | the recovery key | made outside the signer, kept by a person | emergency key | stays offline on USB (comment 19858505) | — |
| naru | host certificates, a year with no renewal | `naructl signer rotate-host`, run by a person | renewed by hand | renewed by `naru-hostd` | naru#412 |
| naru | `caller` certificates, 90 days | `naructl caller credential` | issued by hand | registration and self-renewal | naru#409 |
| naru | `*.m.garam.sh` certificate | a file; `proxy.md`: "DNS-01 issuance needs a DNS provider's credential, which is not granted" | hand-placed certificate | ACME DNS-01 with a Cloudflare token limited to the `m.garam.sh` zone's DNS, issued at the registration step (comment 19858505) | naru#412 |
| naru | enrollment keys, up to 365 days | `orch.md` §Enrollment | long-lived registration secret | one-time, a day at most | naru#412 |
| codehub | `CODEHUB_AUTHZEN_KEY_FILE` | aurora's `aurora/signing-key/codehub-authzen-key` | hand-made key | Transit `codehub-authzen`, JWKS | codehub#327, aurora#864 |
| codehub | `CODEHUB_REGISTRY_KEY_FILE` and its derived JWKS | aurora's `aurora/signing-key/codehub-registry-signing` | hand-made signing key | a Transit key, rotated, JWKS; aurora ADR 0037 declares none for it | codehub#327, aurora#864 |
| codehub | `CODEHUB_AUTHZEN_CLIENT_SECRET`, the runner's `--client-secret-env` | `docs/guide/deployment.md` | shared secrets | `private_key_jwt`, registration | codehub#327 |
| codehub | `deploy/helm/tests/kind-faults.sh`, `dev/gaon/run.sh` | `deploy/helm/tests/`, `dev/gaon/` | shell key generation in test fixtures | material in memory | codehub#327 |
| aurora | `openbao-root`, five years, `rotationPolicy: Never` | `kubernetes/applications/openbao.yaml` | a root that never rotates | rotating, or the reason recorded | aurora#864 |
| aurora | the PKI root (twenty years) and its intermediates `pki_garam_machine` (ten years) and `pki_int` (five years) | `platform/openbao/pki.tf`, aurora ADR 0037 | certificates re-signed by a person's plan, not rotated; the first is due in 2031 | re-signed on a schedule, or the reason recorded | aurora#864 |
| aurora | the committed `garam-service-keys` | `kubernetes/components/garam/service-keys.yaml`, replaced by hand with each key | hand-placed trust | rendered from Transit, or each service's JWKS URL | aurora#864, garam#2061 |
| aurora | `garam-trust` in `cowork` and `codehub` | rendered from the policy CA's and the server root's certificates in KV | hand-placed trust | trust-manager's `aurora-internal-ca` | aurora#864 |
| aurora | the Transit key `aurora-garam-machine`, with the `garam-machine` role and policy | `platform/openbao/garam-machine.tf` | declared, read by nothing | used by garam, or removed (aurora `secrets.md`'s open question) | aurora#864 |
| aurora | the Transit keys `garam-kek`, `garam-issuer`, `garam-policy-feed`, `garam-message`, `codehub-authzen`, `cowork-deployment` | `platform/openbao/transit.tf`, aurora ADR 0037 | built, read by nothing until the switch | read by their consumers through roles from the PM's map | aurora#864 |
| aurora | RustFS's pairs `rustfs/user/root`, `rustfs/user/codehub`, `rustfs/user/qnt-github-actions` | OpenBao KV, each pair made by a person (aurora `secrets.md` §RustFS's credentials) | hand-made credentials | generated in place and rotated | aurora#864 |
| aurora | OpenBao's unseal and recovery output | sauce's USB, `clusters/aurora/openbao-init.txt` | emergency keys | stay offline on USB (comment 19858505) | — |
| aurora | `github/token/aurora-argocd`, `proxmox/api-token/infra`, `cloudflare/token/aurora-cert-manager`, `minimax/api-key/garam`, `google/client/garam-console-dev` | AWS Secrets Manager and OpenBao | external service tokens | stay, handled by people | — |

- **sauce-level items, listed and not filed:**
  - the KMS root's key policy, which names who may sign and decrypt with it;
  - issuing naru's Cloudflare DNS token at the registration step (comment 19858505);
  - registering garam's official app with GitHub, and later with each other service (B1), and the Apple Developer membership (5.9): both decided when goal ⑤ starts (comment 19858505).

## Rationale

- ADR 0031: the bot is the unit, an agent is its harness, and garam is optional.
- ADR 0032: interface 1, one schema for a file and a CRD.
- ADR 0034: why the workspace provider is a Go driver interface, and why a naru Machine is its first production implementation.
- ADR 0035: why the agent interface is an HTTP protocol through `Dial`, and why tools are MCP servers outside the workspace.
- ADR 0036: why a bot is declared anywhere and managed in garam, why one connector per place, and why only pull.
- ADR 0037: why OAuth and OIDC standards, why one signing key per purpose, why no key is made by hand, and why recovery keys stay offline.
- ADR 0038: why garam brokers external apps through one official app per service, with grants in IAM.
- ADR 0039: why one daemon is the connector of a PC and a naru-host place, and why the PC app only shows and drives it.
- ADR 0040: why `cmd/sherlock` goes, and what moves under `garam-bot`.
- garam-bot-operator ADR 0087 to ADR 0093 and garam ADR-0184 to ADR-0188: the cluster's connector and registration, which this design widens to every place.

## Open questions

- **The place record's GRN.** Whether garam widens `grn:<org>/iam/cluster/<id>` with a kind or takes a new type for every place is garam's, in 5.1.
- **A place-declared bot's identity.** Whether a bot declared at its place gets a garam identity is open with garam: garam#2049 leaves it out of scope, and garam-bot-operator `connector.md` keeps it open. Until garam decides, such a bot has no garam token, no messages from garam and no `garam` tool mode.
- **A DPoP proof's way from a bot's process to its connector.** The connector makes the exchange, and only the bot's process can sign its proof. In a cluster they are two pods, and garam-bot-operator#443 writes a bot's token into a Secret the runtime mounts; on a PC and at a naru-host place the daemon is the bot process's parent. How the proof reaches the connector, and the token returns, at each kind of place is decided with garam-bot#1207.
- **Who presents garam's bound model and tool tokens.** The bot's process itself, or the place's gateway and tool proxy asking it to sign each proof, is settled with G3 and G6. The key does not leave the bot's process either way.
- **Messages to a bot.** Whether a bot's process long-polls garam's mailbox itself or its connector carries messages for every bot it runs is not decided. Either is pull. The first is simpler; the second keeps one connection per place.
- **Interface 5.** The model gateway's specification, and where it runs at each place, are not written (G3).
- **The bot's memory.** Whether the memory store ADR 0040 keeps becomes the bot-memory interface, and where it lives at each place, is not decided.
- **Density.** How many bots and workspaces a PC, a naru-host place and a cluster pod run is not measured (interface 2's open questions).
