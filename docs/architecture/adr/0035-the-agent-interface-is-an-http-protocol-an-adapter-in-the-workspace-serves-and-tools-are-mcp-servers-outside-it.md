# ADR 0035: The agent interface is an HTTP protocol that an adapter in the workspace serves, and tools are MCP servers outside it

> Status: accepted
> Date: 2026-11-24

## Context

- ADR 0031 names interface 2, the agent: the harness a bot uses, Claude Code or Codex first, which runs inside the workspace while the bot's process runs outside it. `garamsh/garam-bot#1193` lists start, task, events and stop, with the tools and memory hooks.
- The owner decided the model and tools path on 2026-11-21 (`garamsh/discussions` discussion 6, comment 19791501): tools are MCP, with one Tool interface and three auth modes (garam, secretRef, federated); no Connection resource; no secret in a workspace; one brain per session, other agents being MCP tools; model calls go through interface 5 with a short-lived token.
- The bot's process and the harness sit on two sides of an isolation boundary, a microVM's among them, so the interface crosses it as a protocol, not as a Go call. Interface 3's `Dial` carries a connection across it on every provider.
- The rule of the bot system is that what crosses a boundary toward garam is pulled, not pushed (the owner's approved structure of 2026-11-24, comment 19856107).

## Decision

1. **Interface 2 is an HTTP protocol, `agent/v1alpha1`, that an adapter in the workspace image serves on port 7700,** reached by the bot's process through interface 3's `Dial`. Its types are the Go types in `api/agent/v1alpha1/`. A session runs one harness, a task carries inputs from the bot's mailbox, and the events (messages, tool calls, results and one `taskDone` per task) are read by long poll.
2. **The adapter takes a request only with the workspace's own token,** a file the bot's process writes into the workspace and replaces at least every hour, which grants nothing outside it. The token, not the connection, is the bar, since a provider may serve the port to more than its caller.
3. **Every tool is an MCP server outside the workspace, with a token file.** The tool's own credential, whichever of garam, secretRef or federated it is, is held and added at the execution place, never in the workspace. The model is interface 5's gateway, with a token file read again for each request, billed by API with no subscription login, and an organization may bring its own key (comment 19858505). The workspace reaches both by its egress rules.
4. **The bot's memory is kept by the bot's process from the events,** not by the harness.
5. **The reference implementation is `agentfake`; the first production implementations are the claude-code and codex adapters.**

## Consequences

- A harness that speaks neither HTTP nor MCP needs an adapter that does, and the adapter is a program in the workspace image.
- The bot's process needs an MCP proxy at the place to add each tool's credential, and it is where a bot's tool permissions are enforced.
- The token files are rewritten by the bot's process through interface 3's `WriteFile` before they expire.
- A session's events live as long as the session, so a long session holds them all until a bound is added.

## Signs the decision was wrong

- A harness cannot be pointed at a model base URL with a token read from a file, so MODEL-1 cannot be met without a credential in the workspace.
- The long poll cannot keep up with a harness's events, which would show a stream was needed from the first version.
- Bots need several harnesses in one workspace at once, which would show one session per adapter was too narrow.
- Tool credentials leak into the workspace through the harness's own configuration, which would show the proxy is not where the boundary holds.
