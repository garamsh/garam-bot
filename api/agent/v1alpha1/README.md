# Agent v1alpha1

The normative specification of garam-bot's interface 2, the agent, at version `v1alpha1`: the protocol between a bot's process and the adapter that runs an agent harness, such as Claude Code or Codex, inside the bot's workspace. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Transport
- Authentication
- Sessions
- Tasks and events
- Model and tools
- Credentials in the workspace
- Versioning and compatibility
- Open questions

## Scope and implementations

- **The bot is the unit, and the agent is its harness** (ADR 0013). The bot's process runs outside the workspace and holds the bot's identity, memory, permissions and mailbox. The harness runs inside the workspace and does the work.
- **The adapter** is the program in the workspace image that serves this protocol and drives one harness. The caller is the bot's process, at the execution place, which reaches the adapter through interface 3's `Dial`.
- **One brain per session.** A session runs one harness. Another agent a bot works with is an MCP tool of the session, not a second harness in it.
- **What the protocol carries:** a session (who the bot is, its instructions, where its model and its tools are), the tasks the bot gives it from its mailbox, and the events the harness produces. Through the events the bot's process keeps the bot's memory, so the memory belongs to the bot and not to the harness.

The package's types are the schema, as `garamsh/naru`'s `lib/machineapi` holds its API's, and `check.go` holds the rules every adapter applies (`CheckSession`, `TaskRequest.Check`).

| Implementation | Harness | State |
|---|---|---|
| `agentfake` | none: it echoes a task's input and calls no model | the reference implementation, built, passing the suite |
| claude-code | Claude Code | first production implementation, `internal/claudeadapter/`, built; passes the suite against its image with `make test-claude-code` |
| codex | Codex | second production implementation; to build |

The first two implementations the suite runs against are `agentfake` and the claude-code adapter.

## Transport

- **T-1.** The adapter MUST serve HTTP/1.1 on port `Port`, 7700, of the workspace, with every operation under `PathPrefix`, `/agent/v1alpha1/`. The caller reaches it only through interface 3's `Dial`, so the provider carries the connection. The adapter opens no listener beyond that port except loopback-only proxies that forward solely to `model.baseUrl` and the session's tool URLs. Each binds 127.0.0.1 on a port the kernel picks, refuses any upstream but the one configured and follows no redirect, reads its token file for each request, and logs neither a token nor a header. Anything in the workspace can reach those loopback ports, which grants it no more than the token files it can already read: the adapter and its harness run as one numeric user, so the harness can read those files too.
- **WIRE-1.** Bodies are JSON. The adapter MUST refuse a request body naming a member it does not know with 400 `invalid`, so a caller newer than the adapter learns that its member was not taken. The caller MUST ignore a member it does not know in an answer.
- Every refusal is `ErrorBody`, `{"error": <code>, "message": <text>}`. The codes are `Code`'s. A caller acts on the HTTP status when it does not know the code.

| Call | Meaning |
|---|---|
| `GET .../info` | The harness's name and version, and the release the adapter implements (INFO-1). |
| `PUT .../sessions/{session}` | Start a session with a `SessionSpec`. 201 created, 200 the same spec, 409 `conflict` another spec, 409 `busy` another session lives (SES-1). |
| `GET .../sessions/{session}` | The session's spec and state (SES-2). |
| `DELETE .../sessions/{session}` | End the session, stopping its task. 204, idempotent (SES-3). |
| `POST .../sessions/{session}/tasks` | Give the harness a `TaskRequest`. 202 (TASK-1). |
| `POST .../sessions/{session}/tasks/{task}/stop` | Stop the task. 202 (TASK-2). |
| `GET .../sessions/{session}/events?after=<seq>&wait=<seconds>` | The session's events after `after`, holding up to `wait` seconds for one (EVT-1). |

## Authentication

- **AUTH-1.** Every request MUST carry `Authorization: Bearer <token>`, where the token is the content of `TokenPath`, `/run/garam-bot/agent/token`, in the workspace, surrounding white space removed. The adapter MUST read the file at each request and MUST answer a request without that token with 401 `unauthorized`.
- The caller makes the token, at least 128 bits from a cryptographically secure source, and writes it with interface 3's `WriteFile` before it first reaches the adapter. It replaces it at least every hour, as it does the other tokens (CRED-1), so a token read out of the workspace is worth an hour at most. It is the workspace's own and grants nothing outside it: it is not a garam credential.
- The token, not the connection, is the bar. A provider may serve the adapter's port to more than the caller: naru-proxy serves a Machine's services to whoever reaches it (interface 3's DIAL-1).

## Sessions

- **INFO-1.** `GET .../info` MUST answer the harness's name, which a Bot's `spec.agent.harness` names, its version, and the `SpecRelease` the adapter implements.
- **SES-1.** A session is identified by the caller with a DNS label. `PUT` is idempotent by identifier: the same spec answers 200, another 409 `conflict`. An adapter holds one session at a time, and MUST answer a `PUT` for another identifier while one lives with 409 `busy`. It MUST refuse with 400 `invalid` a spec that `CheckSession` refuses: no bot name, a model or tool URL that is not absolute `http` or `https`, a token file that is not an absolute, clean path, a tool name empty or repeated, or a working directory that is not an absolute, clean path.
- **SES-2.** `GET` answers the session's identifier, spec and state, `idle` or `working` with its task, and MUST answer an identifier it does not hold with 404 `not-found`.
- **SES-3.** `DELETE` ends the session, stopping its task, and is idempotent. Afterwards `GET` MUST answer 404 and a new session MUST be taken.
- The adapter starts the harness for a session in `workingDir`, interface 3's data volume `/workspace` when empty, with `instructions` as its system prompt or its equivalent.

## Tasks and events

- **TASK-1.** A task is identified by the caller, 1 to 128 of `[A-Za-z0-9._-]`, and carries one or more inputs from the bot's mailbox. `POST` is idempotent by identifier: the same body answers 202 and runs nothing a second time, and another body MUST be answered 409 `conflict`. One task runs at a time, and a `POST` of another while one runs MUST be answered 409 `busy`.
- **TASK-2.** A stop of a running task MUST end it, with the outcome `stopped`. A stop of a task that has ended answers 202 and changes nothing, and one of a task the session does not hold is 404.
- **EVT-1.** A session's events are numbered from 1 without a gap, in the order they happened. A read answers the events after `after`, 0 when absent, oldest first, with `next`, the number to read after next. When none is there, the adapter MUST hold the read up to `wait` seconds, 0 to `MaxWait` (50) and 50 when absent, and answer as soon as one is added. A `wait` outside that range is 400 `invalid`. The events live as long as the session.
- **EVT-2.** Every task the adapter took MUST end with exactly one `taskDone` event, its last, whose outcome is `completed`, `failed` or `stopped`, with a reason for the last two.
- **MEM-1.** The adapter MUST report the harness's answers as `message` events and its tool calls and their results as `toolCall` and `toolResult` events, each naming its task. The bot's process keeps the bot's memory from them.

## Model and tools

- **MODEL-1.** The harness MUST send its model requests only to `model.baseUrl`, interface 5's model gateway, carrying the token in `model.tokenFile` read again for each request. The adapter MUST NOT give the harness any other model credential, and the harness's login, a subscription's among them, is not used. Model calls are billed by API through the gateway only, and an organization may bring its own provider key, which the gateway holds and the workspace never sees (the decision of 2026-11-24 recorded at `garamsh/discussions` discussion 6, comment 19858505).
- **The model gateway and the tool servers are reached by the workspace's egress.** Their URLs are addresses the workspace's egress rules allow (interface 3's NET-1), so the place runs them where its workspaces can reach them.
- **TOOL-1.** The harness's tools are the MCP servers of `tools`, over MCP's Streamable HTTP transport, and the harness's own tools inside the workspace (its file edits and its shell). The adapter MUST NOT configure an MCP server the session does not name. Each request to a server carries the token in its `tokenFile`, read again for each request.
- **One Tool interface, three ways of authenticating, all outside the workspace.** Every tool reaches the harness as one MCP server URL with a token file, whatever it is. What the tool itself authenticates with is decided at the execution place, where the bot's process, or a proxy of its own, holds it and adds it to each call:
  - `garam`: a short-lived token garam issues for the bot and the tool, narrowed to the bot's scope (`docs/architecture/bot-system.md` §External apps, garam-bot#1208);
  - `secretRef`: a secret the declaration names by reference (interface 1's SEC-1), read at the place;
  - `federated`: a token exchanged at the tool's own issuer for the bot's.

  A tool is not a resource of its own: there is no Connection kind here or in garam. An organization's installation of garam's app for a service, and each bot's grant to use it, are garam IAM's (`docs/architecture/bot-system.md` §External apps).

## Credentials in the workspace

- **CRED-1.** No secret enters a workspace. The caller MUST NOT write any credential into it but the tokens this protocol names: the adapter's token, the model token and the tool tokens. Each MUST live at most an hour, and the caller rewrites it before it expires. The bot's own token is an RFC 8693 exchange made at the place (`docs/architecture/bot-system.md` §Authentication, garam-bot#1208), and stays there.

## Versioning and compatibility

- **A release of `v1alpha1` may add** an operation, an optional member of a request, a member of an answer, an event type, a session state or an error code. An adapter refuses a request member it does not know (WIRE-1), so a caller sends a member only to an adapter whose `specRelease` names it.
- **A new version is required** to remove or rename anything, make a member required, tighten a rule of `check.go`, or change what an operation means, its idempotency or the state it leaves after a failure. A new version is served at a new path, beside the earlier one while callers move.
- **VER-1.** Each release has a number, `SpecRelease` in this package, from `0.1.0`, which `info` answers, and the `api/` module's tag moves with every release.
- **VER-2.** A caller MUST skip an event type it does not know, and read a session state it does not know as `unknown`. This is what makes adding either a compatible release.
- **A deprecated element** is marked so here and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the suite: the claude-code and codex adapters.

## Open questions

- **The terms of service of wrapping Codex.** Billing is decided: by API through the gateway, with no subscription login (comment 19858505). For Claude Code the check is recorded on garam-bot#1210: an organization's own API key through the gateway is the one mode this version supports, under the Commercial Terms, with the binary unmodified and its built-in authentication neither removed nor disabled. Whether garam may pay for a bot's usage itself is not decided and nothing here builds it. The Codex adapter's issue checks its own before it ships.
- **Density.** How many workspaces, and so how many harnesses, an execution place runs at once is not measured. The PC daemon's and the cluster's targets wait for a measurement.
- **Several sessions in one adapter.** This version holds one. A workspace shared by bots (interface 1's `SharedRead`) does not need more, since each bot has its own workspace for its harness.
- **Events past a session.** The events live as long as the session. A bound on how many it keeps, with 410 for a cursor before it, is an addition.
- **Approvals.** A harness that asks before a tool call is answered by the session's permissions, which the bot's process enforces at its tool proxy. An event that asks the bot's process to approve a call is an addition.
