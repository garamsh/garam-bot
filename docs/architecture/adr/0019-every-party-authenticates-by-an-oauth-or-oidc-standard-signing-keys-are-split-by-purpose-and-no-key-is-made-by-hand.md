# ADR 0019: Every party authenticates by an OAuth or OIDC standard, signing keys are split by purpose, and no key is made by hand

> Status: accepted
> Date: 2026-11-24

## Context

- **Authentication** (sauce's approved structure, `garamsh/discussions` discussion 6, comment 19856107):
  - people sign in by OIDC with PKCE;
  - the CLI and the PC app by device authorization (RFC 8628);
  - connectors by `private_key_jwt` (RFC 7523) for one-hour tokens;
  - bots by short tokens their connector obtains by token exchange (RFC 8693);
  - signing keys are split by purpose, rotated automatically and published as JWKS;
  - internal TLS is issued automatically.
- **Key management** (comment 19857399): no key is made, placed or renewed by a person. The root of trust is AWS KMS, which holds no value, and people handle only it and external service tokens (GitHub, Proxmox, Cloudflare). Signing keys and KEKs are held in OpenBao Transit or KMS. Internal TLS comes from cert-manager, or OpenBao PKI. The OIDC issuer rotates its own key. Connectors register with a token and a key made on site. Shell key generation goes.
- **The remaining items, decided on 2026-11-24** (comment 19858505):
  - OpenBao's recovery keys and naru's recovery key are emergency keys the system makes once and uses only in a disaster, kept offline on USB;
  - naru's `*.m.garam.sh` is issued by ACME DNS-01, with a Cloudflare token limited to DNS edits in the `m.garam.sh` zone, which sauce issues at the registration step at the end of the build.
- **Bound bot tokens** (sauce, 2026-11-25, comment 19863377): a bot token a holder can use until it expires is usable if it leaks, so a bot's token is bound to its process's key, by DPoP (RFC 9449). The PM extended it to every token garam issues to a bot's process, and chose DPoP over mTLS binding (garam#2078).
- **What garam has** (garam at `dc030cd1c48b8324b207a65a2606448ecb9203ea`):
  - OIDC with PKCE against Google;
  - the CLI's device grant (garam ADR-0120);
  - an OIDC issuer (garam ADR-0141);
  - a cluster's `private_key_jwt` exchange (garam ADR-0184);
  - Transit for message signing keys (garam ADR-0104).
- **What aurora has built** (aurora ADR 0019, at aurora `2de277ad451ea8c543801b440096124c09603c9f`): Transit keys for garam's KEK, issuer, policy feed and message signing and for codehub's and cowork's assertions, a PKI root with two intermediates, cert-manager issuers on them and a trust bundle, all read by nothing until garam's switch release.
- **The issuer's key is not its own.** garam's OIDC issuer signs with the policy feed's key (`docs/architecture/deployment-contract.md` §The identity issuer is off until it is named), and `deploy/charts/cluster/key-material.sh` makes every internal key and certificate with `openssl`.

## Decision

1. **The standards:**
   - **People:** OIDC authorization code with PKCE.
   - **The CLI and the PC app:** RFC 8628 device authorization.
   - **A connector:** `private_key_jwt` (RFC 7523), exchanged for a token of one hour.
   - **A bot garam declared and placed:** a token its place's connector obtains by RFC 8693 token exchange. The connector presents its own access token as the `actor_token`, and a `subject_token` of a garam-defined type whose value names the bot's GRN, with garam's `audience` and an explicit `scope`. The issued token's `sub` is the bot's GRN, `act.sub` the place's GRN, `aud` garam's audience and `scope` what was asked and allowed, and it lasts at most one hour. garam refuses a bot that no live declaration places on that place (garam ADR-0184's decision "A cluster's token reaches the cluster-connection routes", and AUTH-5). The token stays at the place: the workspace holds only narrower model and tool tokens (interface 2's CRED-1). A bot declared at its place gets no garam token.
   - **Binding:** every token garam issues to a bot's process, the exchanged access token and garam's model and tool tokens, is a DPoP token (RFC 9449) bound by `cnf.jkt` to a key the bot's process makes when it starts and never lets leave it. The exchange carries a proof that process signed. A new process makes a new key and a new exchange. garam's resource servers verify each proof, refuse a replayed one, and refuse a bound token presented as `Bearer`. mTLS binding (RFC 8705) is not used: a place does not control where TLS is terminated. No bound token enters a workspace.
   - **A first-party service** (cowork, codehub): `private_key_jwt` with a key held in Transit.
2. **One signing key per purpose:**
   - the OIDC issuer;
   - the policy feed;
   - message signing;
   - codehub's registry tokens;
   - each service's assertion key;
   - naru's authorities.

   Each is held in Transit or KMS and signs there, rotates on a schedule with its predecessor published until that key's tokens expire, and is published as JWKS by the issuer it serves. A program refuses a configuration that gives two purposes one key.
3. **Internal TLS is issued and renewed automatically:**
   - cert-manager, with an OpenBao PKI issuer where a CA is shared, in a cluster;
   - naru's signer on a naru host;
   - ACME for a public name: naru's `*.m.garam.sh` by DNS-01, with a Cloudflare token named for the service that consumes it and limited to DNS edits in the `m.garam.sh` zone. Until sauce issues it at the registration step, the hand-placed file stays.

   A program re-reads its pair on renewal.
4. **No key is made by hand.** Every key-material script goes. A person holds only the KMS root and external service tokens. OpenBao's recovery keys and naru's recovery key are emergency keys, kept offline on USB, and do not move to KMS. Only day-to-day operating keys are in KMS and Transit.
5. **The work is one issue per repository, in this order:**
   1. garam (garam#2061);
   2. naru (naru#412) and codehub (codehub#327);
   3. garam-bot (garam-bot#1207);
   4. aurora's switch (aurora#864), at the registration step at the end of the build.

   Each lands behind configuration that keeps today's path until aurora switches.

## Consequences

- Transit and cert-manager are dependencies of every deployment, and a local run needs an OpenBao dev server or material made in memory.
- A token's verifier fetches JWKS and follows rotation, and a hand-pinned root goes.
- A stolen bot token is useless without the bot process's key. In exchange, the bot's process signs a proof for every request, and its connector needs a proof from it for every exchange.
- Operator PEM issuance and download leave garam, and the manager and the control service authenticate through the connector.
- garam-bot#1207 moves the manager and the control service onto the registration path. It lands with the later ADR that garam-bot-operator ADR 0093's decision "The manager's and the control service's issued keys do not meet this rule yet" waits for, and that ADR also replaces ADR 0090's decision "The control-service path is not part of the connector", which leaves retiring that path "a later decision". Its number is reserved when garam-bot#1207 starts.

## Signs the decision was wrong

- Transit's latency or availability refuses token issuance often enough that a key in memory is put back.
- A party cannot use a standard flow (a device without a browser for RFC 8628, a harness without token files for RFC 8693's tokens) and a bespoke one returns.
- A key the inventory missed is found made by hand after the registration step.
