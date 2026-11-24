# ADR 0037: Every party authenticates by an OAuth or OIDC standard, signing keys are split by purpose, and no key is made by hand

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
- **Key management** (comment 19857399): no key is made, placed or renewed by a person. People handle only AWS KMS, the root, and external service tokens (GitHub, Proxmox, Cloudflare). Signing keys and KEKs are held in OpenBao Transit or KMS. Internal TLS comes from cert-manager, or OpenBao PKI. The OIDC issuer rotates its own key. Connectors register with a token and a key made on site. Shell key generation goes.
- **What garam has** (garam at `617686ff296df67b22c23699555310949f00861d`):
  - OIDC with PKCE against Google;
  - the CLI's device grant (garam ADR-0120);
  - an OIDC issuer (garam ADR-0141);
  - a cluster's `private_key_jwt` exchange (garam ADR-0184);
  - Transit for message signing keys (garam ADR-0104).
- **The issuer's key is not its own.** garam's OIDC issuer signs with the policy feed's key (`docs/architecture/deployment-contract.md` §The identity issuer is off until it is named), and `deploy/charts/cluster/key-material.sh` makes every internal key and certificate with `openssl`.

## Decision

1. **The standards:**
   - **People:** OIDC authorization code with PKCE.
   - **The CLI and the PC app:** RFC 8628 device authorization.
   - **A connector:** `private_key_jwt` (RFC 7523), exchanged for a token of one hour.
   - **A bot:** a token of at most one hour that its connector obtains by RFC 8693 exchange of its own token, naming the bot, with the place as the actor. It stays at the place: the workspace holds only narrower model and tool tokens (interface 2's CRED-1).
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
   - ACME for a public name.

   A program re-reads its pair on renewal.
4. **No key is made by hand.** Every key-material script goes. A person holds only the KMS root, naru's recovery path and external service tokens.
5. **The work is one issue per repository, in this order:**
   1. garam (garam#2061);
   2. naru (naru#412) and codehub (codehub#327);
   3. garam-bot (garam-bot#1207);
   4. aurora's switch (aurora#864), at the registration step at the end of the build.

   Each lands behind configuration that keeps today's path until aurora switches.

## Consequences

- Transit and cert-manager are dependencies of every deployment, and a local run needs an OpenBao dev server or material made in memory.
- A token's verifier fetches JWKS and follows rotation, and a hand-pinned root goes.
- Operator PEM issuance and download leave garam, and the manager and the control service authenticate through the connector.
- garam-bot-operator ADR 0093 decision 5 is resolved by garam-bot#1207: the manager and the control service move onto the registration path.

## Signs the decision was wrong

- Transit's latency or availability refuses token issuance often enough that a key in memory is put back.
- A party cannot use a standard flow (a device without a browser for RFC 8628, a harness without token files for RFC 8693's tokens) and a bespoke one returns.
- A key the inventory missed is found made by hand after the registration step.
