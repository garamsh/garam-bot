# ADR 0038: garam brokers external apps through one official app per service, scopes in garam IAM, and short narrowed tokens

> Status: accepted
> Date: 2026-11-24

## Context

- **sauce decided on 2026-11-24** (`garamsh/discussions` discussion 6, comment 19857097):
  - for each external service, an organization installs garam's official app once;
  - garam IAM (GRN, AuthZEN) decides which bot uses which connection with which scope;
  - garam issues short tokens narrowed to that scope, as a GitHub installation token narrowed by `repositories` and `permissions`;
  - a person's OAuth is used only when a bot acts for that person;
  - an organization may bring its own app;
  - the broker is part of this design.
- **Interface 2** reaches every tool as an MCP server outside the workspace, with a tool's credential added at the place under one of three modes: garam, secretRef, federated (ADR 0035). No secret enters a workspace.
- **garam has the pieces for the decision:** GRNs for every resource (garam ADR-0142), its policy service answering AuthZEN evaluations, and a token endpoint. Whether a token carries who a bot acts for is open in garam#1787.

## Decision

1. **One official app per external service, installed once per organization.** GitHub is first. garam holds each installation as a connection, a GRN of the organization, and the app's private key in Transit, signing the app's JWTs there.
2. **The scope lives in garam IAM.** A policy names which bot may use which connection with which scope (for GitHub: which repositories, which permissions). garam's policy service decides each request by an AuthZEN evaluation.
3. **garam issues short narrowed tokens.** A bot's tool proxy at the place exchanges the bot's token by RFC 8693, naming the connection and the scope. garam checks the policy, mints the external service's narrowest token (a GitHub installation token with `repositories` and `permissions` set), and answers it with its expiry, at most an hour. garam calls the external service and never the place.
4. **A person's OAuth only on that person's behalf.** A task that acts for a person carries that person, and garam uses the person's own grant for the service only then and only if the person granted it.
5. **An organization may bring its own app,** registered in garam with its key placed into Transit, and used by the same policy and the same exchange.
6. **At the place, this is interface 2's `garam` mode.** The token goes to the tool proxy, not into the workspace.

## Consequences

- A bot never holds an installation's or a person's long-lived credential, and revoking a policy stops the next token.
- garam keeps the external apps' keys and the people's grants, so garam's Transit and its database carry them.
- A service without narrowable tokens gets no broker mode until garam can narrow its token another way, and it stays on secretRef until then.
- Registering garam's official app with each service is a person's act at that service, once.

## Signs the decision was wrong

- Scopes in garam IAM drift from what the external app was granted at install, so a token garam asks for is refused often enough to confuse users.
- Organizations bring their own app by default, which would show the official app is not what they trust.
- The exchange's latency on every tool call is felt, which would show the token wants caching at the place within its lifetime.
