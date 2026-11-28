# ADR 0044: Interface 5 is a model gateway at the place that issues its own token and holds the organization's own key

> Status: accepted
> Date: 2026-11-29

A record states one current decision and is kept current: update the body when the decision evolves; delete the record when the decision stops carrying meaning. Numbering stays continuous — a deletion or insertion renumbers the log, and references in other records are updated in the same PR. History lives in git, not in the log.

## Context

- **Interface 5 is one of garam-bot's interfaces** (ADR 0031), and is not written (garam-bot#1211, G3).
- **What the workspace already does.**
  - A session's harness sends its model calls to interface 2's `model.baseUrl` with the token of `model.tokenFile`, read for each call (MODEL-1).
  - The claude-code adapter forwards the Anthropic Messages API there, path kept, with `Authorization: Bearer <token file>` (ADR 0035).
  - The bot's process writes the token, which its connector obtains from an issuer at the place (interface 7's TOK-1, ADR 0043 decision 6). No production issuer exists (garam-bot-operator ADR 0099 decision 9).
- **garam's record.** garam ADR-0190 makes ai-gateway an interface deployable at `ai.garam.sh` or in the organization's environment, with BYOK, and a call carrying a short-lived token bound to the gateway's audience. Its decision 2 has a garam-declared bot obtain that token by exchanging its bot token at garam.
- **Why that exchange cannot serve at the place:**
  - a bot declared at its place has no garam token (`bot-system.md` §Authentication);
  - every token garam issues to a bot's process is bound to the process's key (garam ADR-0191), so none may enter a workspace;
  - bring-your-own-key at the place needs no garam.
- **v1 is BYOK alone** (cupa on garam-bot#1211, 2026-11-28). Where garam pays on an organization's behalf waits for an agreement with the provider, and needs only a seam now.
- The PM approved this design on garam-bot#1211 on 2026-11-29, with its answers (i) to (ix).

## Decision

1. **Interface 5 is `api/model-gateway/v1alpha1/`, with two surfaces.**
   - **The model surface** is the provider's own API, passed through at `model.baseUrl`, with a model token as a bearer. `v1alpha1` names Anthropic Messages alone.
   - **The issue surface**, `POST <base>/model-gateway/v1alpha1/model-tokens`, is called by the place's issuer with a credential of the place naming it, and answers a model token for a bot.
   - **Two profiles** (PRO-1, PRO-2). Every gateway serves the model surface. A gateway whose tokens its place's issuer obtains from it serves the issue surface too. `ai.garam.sh`, whose tokens are garam's exchange, serves the model surface alone.
2. **The model token is stated abstractly** (TOKEN-1): it lives at most an hour, is of the gateway's own audience, is checked by that gateway alone, and is opaque to every other party.
   - **At the place**, the gateway issues it, through the issue surface.
   - **At `ai.garam.sh`**, it is garam ADR-0190's exchange of the bot's token, which TOKEN-1 admits.
   - garam records the place's form in garam ADR-0195, which supersedes garam ADR-0190 decision 2 in part.
3. **A token outlives its gateway's restart** (TOKEN-2). The bot's process renews after 40 minutes at most, so a restart that voided tokens would leave every workspace without a model until then. At a cluster the signing key is in a Secret the gateway writes. A gateway that rotates its key goes on accepting the earlier key's tokens for at least an hour after.
4. **The gateway holds the organization's own key, and v1 serves no other** (KEY-1, KEY-2). A gateway with no key serves no model call. The key reaches no workspace, harness, answer or log, and the gateway forwards none of a call's headers but the provider API's own, so a workspace's credential reaches no upstream (MOD-2). Platform billing is a later key or upstream the gateway chooses per bot, behind the same surfaces.
5. **Usage is recorded per bot** (USE-1): the bot, the model, the status and the provider's token counts, never the content. How a deployment keeps the records is its own, and reporting them to garam is an open question of interface 5.
6. **Both surfaces are limited**, each bot's model calls and each issuer's token calls, and a refusal costs no other bot (LIMIT-1, LIMIT-2).
7. **The reference implementation is `gatewayfake`**, which passes the server suite in `make ci` and is the stub interface 2's MODEL-1 case runs against.
8. **At a cluster**, the gateway is a program of its own beside the connector, so the provider's key stays out of the program holding the cluster's identity (garam-bot#1211 PRs (b) and (c), garam-bot-operator ADR 0100). naru Machines, which run outside the cluster, reach a gateway the installer names, in v1.

## Consequences

- garam's `ai-gateway` builds to this specification (garam ADR-0190 §Consequences), with its own token.
- The connector's issuer obtains a bot's model token from the gateway. A Bot that declares tools waits for the tool proxy (G6), since TOK-1 answers a token per tool.
- `v1` needs two production implementations passing the suite in the model profile, the place's gateway and `ai.garam.sh`, and the place's gateway passing it in the place profile too.
- A model token is a bearer within its hour: one read from a workspace can be presented from elsewhere until it expires.
- A deleted Bot's model tokens go on working until they expire, an hour at most. Refusing them sooner is an open question of interface 5.

## Signs the decision was wrong

- **Organizations want garam's records and budgets** for calls through their own gateway often enough that interface 5 grows a reporting path, which would show the place's gateway should have reported from the start.
- **A token outliving a restart is not enough**: restarts are frequent enough that tokens are refused anyway, which would show the token belonged to a key the place shares, not the gateway's own.
- **The model surface's pass-through breaks** on a harness that needs a header MOD-2 drops, often enough that the allow list is read around.
- **Bearer tokens read from workspaces are used elsewhere**, which would show the token needed binding to the workspace.

## Rejected alternatives

- **garam ADR-0190's exchange at every place.** A bot declared at its place has no garam token, and a garam token bound to the process's key may not enter a workspace, so the place would depend on garam for a call that needs no garam.
- **The connector signs the model token.** The gateway would check a key the connector holds, and the connector, which holds the cluster's identity, would be the one place both a token's issue and its check could be forged from. The gateway issuing its own keeps each key with the one program that uses it.
- **The gateway inside the connector.** One program would hold the provider's key and the cluster's identity, so a fault in either surface exposes both.
- **The provider's key in the workspace**, with no gateway. It breaks the design bar: a long-lived secret in a workspace, readable by every harness that runs there.
- **A gateway that forwards a call's headers but its credentials.** A header the harness or a workspace's process adds reaches the provider unread. An allow list of the provider API's own headers is the smaller surface.
