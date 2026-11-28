# Agent v1alpha1 conformance

The tests of `README.md`'s requirements: what each one is, and how a new adapter runs them.

## Running the suite

- **The suite** is `conformance/`: every case in `Cases()`, each a subtest named `<requirement>/<case>`. An adapter plugs in as a `conformance.Subject` (the base URL that reaches its port, the HTTP client, the token of `TokenPath`, and a session spec whose model and tools reach what its harness needs) and runs `conformance.Run(t, subject)`. The suite reaches the adapter over HTTP alone.
- **The cases run one after another** against one adapter, which holds one session at a time, and each deletes the session it made.
- **`TestReference`** runs it against `agentfake`, served by `httptest`. `make test` runs it.
- **A real adapter** runs it in a workspace of interface 3, with the conformance session's model a gateway that answers, the token written with `WriteFile`, and the base URL reached through `Dial`. The inputs are plain requests, "Reply with the word ready.", and `conformance.HoldInput`, which the harness is asked to keep working on until it is stopped.
- **The caller suite** is `CallerCases()` in the same package, run with `conformance.RunCaller(t, caller)`: the cases of the requirements that bind the caller, VER-2 and CRED-1. A caller plugs in as a `conformance.Caller` (read the events of a session, read its state, keep the workspace's tokens), and the suite serves it a scripted adapter that says what a later release may, and a recording workspace with a clock it moves 10 minutes at a time over 3 hours. It reaches no real adapter and no workspace.
- **`TestReferenceCaller`** runs it against `conformance.ReferenceCaller`, the smallest caller that keeps the obligations. The bot's driver binds its own code to the same cases in `internal/bot`'s `TestDriver_KeepsTheCallersRequirements`, and does not copy the reference. Its binding holds the events read and the token files of one write. The driver reads no session state, so the state case holds only the binding. The schedule of the rewrites, which the binding's `Keep` copies, is held instead by `TestRenewal_…` and the `TestWorkspace_…` cases against interface 7's reference server.
- **A pending case** is skipped with its reason. It counts as named, not as passing.

## Requirements and their tests

| Requirement | Tests |
|---|---|
| T-1 | Every case: the suite reaches the adapter at `PathPrefix` alone |
| WIRE-1 | `WIRE-1/a body member the adapter does not know is refused` |
| AUTH-1 | `AUTH-1/a request without the workspace's token is refused` |
| INFO-1 | `INFO-1/info names the harness and the release` |
| SES-1 | `SES-1/a session is idempotent by identifier and one lives at a time`, `SES-1/a spec that breaks a rule is refused` |
| SES-2 | `SES-2/a session answers its spec and state` |
| SES-3 | `SES-3/delete is idempotent and frees the adapter` |
| TASK-1 | `TASK-1/a task is idempotent by identifier and one runs at a time` |
| TASK-2 | `TASK-2/a stopped task ends stopped` |
| EVT-1 | `EVT-1/events are numbered from 1 and read after a sequence number`, `EVT-1/an events read holds until an event is added` |
| EVT-2 | `EVT-2/a task's last event is its one taskDone` |
| MEM-1 | `MEM-1/the harness's answer is a message event`. Tool call events wait for TOOL-1's stub |
| MODEL-1 | `MODEL-1/model requests go to the gateway with the token file's token`, pending a model gateway stub, which arrives with interface 5 |
| TOOL-1 | `TOOL-1/the harness reaches only the session's MCP servers, with their token files' tokens`, pending an MCP server stub |
| CRED-1 | `CRED-1/the caller writes no credential but short-lived tokens into a workspace`, in the caller suite |
| VER-1 | `INFO-1/info names the harness and the release` reads the release |
| VER-2 | `VER-2/a caller skips an event type it does not know`, `VER-2/a caller reads a session state it does not know as unknown`, in the caller suite |
