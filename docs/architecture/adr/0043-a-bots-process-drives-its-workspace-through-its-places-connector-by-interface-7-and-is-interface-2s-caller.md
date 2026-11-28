# ADR 0043: A bot's process drives its workspace through its place's connector by interface 7, and is interface 2's caller

> Status: accepted
> Date: 2026-11-28

A record states one current decision and is kept current: update the body when the decision evolves; delete the record when the decision stops carrying meaning. Numbering stays continuous — a deletion or insertion renumbers the log, and references in other records are updated in the same PR. History lives in git, not in the log.

## Context

- The pieces of a running bot exist apart (garam-bot#1281):
  - the manager runs one `garam-bot run` per Bot (garam-bot-operator ADR 0098);
  - the process obtains its DPoP-bound garam token from its connector by interface 6 (ADR 0042);
  - interface 3's Pod provider (`internal/podworkspace`) and naru Machine provider (`pkg/naruworkspace`, run inside the cluster connector) exist;
  - the claude-code adapter serves interface 2 in a workspace (ADR 0035).

  Nothing joins them. No code calls a `Provider` beyond constructing it, and nothing drives an adapter.
- `bot-system.md` has the bot's process reach the adapter through `Dial` (ADR 0035). But the providers' rights sit elsewhere:
  - the naru Machine provider holds the place's naru caller certificate, in the connector;
  - the Pod provider needs `pods/exec`, `pods/portforward` and the right to make Pods, claims and NetworkPolicies.
- **The process could link a provider itself.** That spreads those rights to every bot:
  - a Pod provider under each Bot's ServiceAccount needs a Role per Bot, which the manager would escalate its own rights to create (ADR 0042's context), or one Role shared by a namespace's Bots, under which any Bot can exec into another's workspace;
  - the naru Machine provider needs the caller certificate, which lives 90 days, in every bot's Pod, and one naru caller sees every Machine the place holds.

  Both break the bar the system holds: no long-lived secret in a bot's process or its workspace, and each party holding only what its own work needs (`bot-system.md` §Authentication, interface 2's CRED-1).
- **The manager could serve the workspaces.** It holds workload rights, but it opens no listener and meets the connector only through the Kubernetes API (garam-bot-operator ADR 0090).
- **The connector already serves the bots' processes.** It serves interface 6 over TLS it makes itself, on a credential the kubelet projects into the Bot's Pod alone, and checks that credential against the live Pod (garam-bot-operator ADR 0097).
- The process and the connector are built and released separately, and the PC daemon will be a second connector (ADR 0039). So the call between them is a versioned interface (`docs/convention/interfaces.md`).
- The PM approved this design on garam-bot#1281 on 2026-11-28. The approval added:
  - a second key for the same workload instance is refused with one answer;
  - per-Bot limits as in interface 6;
  - the workspace namespace is dedicated, and each workspace is isolated by its own NetworkPolicy and named per Bot;
  - the model and tool tokens come through a seam whose only issuer, until G3 and G6, is a stub wired in the end-to-end suite;
  - what a connector restart mid-`Dial` does is stated.
- The PM's review of garam-bot#1283 then decided:
  - deletion on evidence alone, with labels naming each workspace's owner;
  - bounded dials, and a request limit per instance;
  - a cut that only a new version widens;
  - writes of several files in one call;
  - recovering a failed workspace;
  - egress per provider;
  - a stub issuer that a released build cannot carry.

## Decision

1. **Interface 7, workspace access, is `api/workspace-access/v1alpha1/`.** A bot's process calls its place's connector to:
   - acquire its workspace;
   - write the workspace's token files;
   - dial its adapter;
   - obtain the session's model and tool tokens.

   The connector links the providers and holds their rights. The process holds no provider right and no provider credential.
2. **The connector answers a Bot for its own workspaces alone.**
   - **Addressing.** A request names one of the Bot's `spec.workspaces` attachments, never a workspace.
   - **Naming.** The connector names the workspace from the authenticated Bot's namespace, name and UID and the attachment. No request can address another Bot's workspace, and a Bot made again under the same name gets a new one.
   - **The spec.** The connector builds it from the attachment's WorkspaceClass and the place's configuration: the image for the Bot's `spec.agent.harness`, by digest (interface 1's IMG-1), interface 2's port, and egress to the place's model gateway and tool proxy.
   - **Labels.** Each workspace carries its owner, the place's release and the Bot's namespace, name and UID, as labels. Interface 3 gains an optional `Spec.Labels` for them in its release 0.3.0, with `Capabilities.Labels` saying whether a provider keeps them. A provider that keeps them answers them in every read as written. One that cannot refuses a spec carrying any.
     - **The Pod provider** keeps them, on its objects and in the spec its claim records.
     - **The naru Machine provider** cannot. `machines/v1alpha1` carries no caller's label on a Machine: its shape's `labels` are values a host must carry. naru#453 asks for caller-set Machine metadata labels, and the provider reports `Labels` true once naru serves them.
     - **The connector** sends labels only to a provider that keeps them.
   - **Deletion.** The connector deletes a workspace only on evidence that its Bot is gone, and only its own place's workspaces. At a cluster, garam-bot-operator ADR 0099 states the evidence: a live read of the Bot, and a grace. A workspace whose labels it cannot read is never deleted by it, so until naru#453 a deleted Bot's naru Machines are deleted by hand.
3. **Each call is cut to what the driver needs:**
   - acquire, which creates or starts the workspace and answers its phase with the session's model and tool URLs;
   - read the workspace's state;
   - write files under `/run/garam-bot/` alone, several in one call, never following a link at a path's final component;
   - dial interface 2's port 7700 alone;
   - obtain the session's model and tool tokens.

   There is no exec, no read of a file, no snapshot and no delete. **The cut is the interface's security property.** A call that runs a command in a workspace, reads from it, writes elsewhere or reaches another port needs a new version of interface 7 and a decision record, never a release of `v1alpha1` (its REACH-1).
4. **The place's credential, and the process's key.**
   - **The credential.** The process authenticates with a place credential of its own audience, `garam-workspace` at a cluster. It is checked as interface 6 checks its own: verified, at most an hour long, and bound to the running workload instance of the Bot. Unlike interface 6's, it is used for each request until it expires, not spent once.
   - **The proof.** Every request also carries a DPoP proof (RFC 9449) signed by the process's ES256 key, the one ADR 0042 makes, bound to the request and to the credential.
   - **The pin.** The connector pins the first key presented for a workload instance, and answers every request from that instance under another key with one refusal, `invalid_key`. It reads the instance again from its source before that refusal, so a restart it has not yet seen is not refused. A place runs one such server at a time, and drops the pins of instances that no longer exist.
   - **Why the first key is the right one.** The credential is projected into that instance alone, so its first presenter is the instance's process. A credential read later, from transit or a log, is worthless without the pinned key.
   - **A restart.** A restarted container is a new instance with a new key, and pins anew. At a cluster the instance is the Pod with its `runtime` container's ID while that container runs.
   - **A refused process waits.** A process answered 401 backs off from 1 second to 60, as interface 6's client does.
   - **Limits.** Each Bot's calls that reach a provider are limited, as interface 6 limits its exchanges, and one Bot's calls cost no other Bot. Each workload instance's requests are limited as well, right after its credential verifies and before anything is read for it, so a process sending refused requests costs only itself.
5. **`Dial` is an HTTP upgrade on interface 7.**
   - **The splice.** The connector calls the provider's `Dial` to port 7700 and splices the two connections. It reads nothing of what passes.
   - **End-to-end authentication.** Interface 2's own bearer token, AUTH-1, still authenticates the process to the adapter, so the connector speaks no interface 2.
   - **A connector restart mid-`Dial`** breaks the spliced connection. The process dials again after a back-off from 1 second to 60. The adapter keeps its session, its running task and its numbered events, so the process reads on from the last event it read and loses nothing.
   - **Bounded dials.** An instance holds at most 4 dials at once, and the server a configured number in all. A dial idle for 120 seconds, above interface 2's 50-second long poll, is closed. Every dial of an instance is closed when the instance, its Bot or the workspace ends, so no dial outlives its authority.
   - **A credential issued before the restart.** The connector refuses it, as interface 6's server does, so its pins need no state. The process waits until the place replaces the credential: at a cluster, the kubelet does so within 8 minutes.
6. **The model and tool tokens come from the place's gateway and tool proxy, through the connector.**
   - **The call.** The process asks interface 7 for the session's tokens, and the connector obtains them for that Bot and workspace from the place's model gateway (interface 5, G3) and tool proxy (G6). Each lives at most an hour and grants nothing outside its own service (interface 2's CRED-1).
   - **Where they go.** The process writes them into the workspace's token files, and the connector keeps none past its answer.
   - **Before G3 and G6.** The connector's issuer is a stub compiled only into the end-to-end suite's build, so a released connector cannot wire it. Without one, the session waits, and the process reports `waitingForGateway`.
   - **The URLs.** The gateway and the tool proxy URLs are the place's configuration, given per provider because a workspace reaches them from where that provider runs it, and answered by acquire.
7. **`garam-bot run` is interface 2's caller.**
   - **Configuration.** It reads `--workspace-credential-file` beside `--connector-url` and `--connector-trust-file`, and drives the Bot's first attachment. A Bot with none, or a process with no connector, runs with no workspace.
   - **The session.**
     1. It acquires the workspace until it runs, reading again no sooner than the `Retry-After` a pending answer carries. A failed workspace is recovered by acquire, which recreates its workload and keeps its data volume.
     2. It writes a fresh adapter token, at least 128 random bits.
     3. It checks the adapter's `info` against `spec.agent.harness`.
     4. It writes the session's tokens, with the adapter token in one write.
     5. It opens the session `bot`, with `spec.persona.instructions` and the model and tool URLs acquire answered.
     6. A session the adapter already holds under another spec, from an earlier process or an earlier generation, is deleted and opened again.
   - **Tasks.** It starts one task per message from its message source, identified from the message, one at a time, and waits for the task's `taskDone` before the next.
     - The source is G4's long poll once garam serves it (garam#2075).
     - Until then, and for a bot with no garam identity, it is a directory of message files, `--inbox-dir`.
   - **Events.** It long-polls them from the last `next` it read, skips a type it does not know, and reads a phase or session state it does not know as unknown (interfaces 2 and 3, VER-2). It hands each event to a sink. Today the sink records the last task's outcome and counts in the status file. The bot's memory takes them when it is decided.
   - **Rewriting the token files.** Every 40 minutes it obtains new session tokens and writes them with a new adapter token in one write, so none it wrote is more than an hour old (CRED-1). A renewal is two calls. Every token is held in memory alone.
   - **Failures.** A workspace, adapter or gateway that fails leaves the bot running, with the reason in the status file, and is retried after a back-off from 1 second to 60.
8. **Nothing is logged but who, what and how it ended.**
   - **Never logged.** No file content, token, credential or proof reaches a log line, metric, event, status or refusal, on either side.
   - **Logged.** The connector logs each call with the Bot, the call and its outcome. The process logs task identifiers, event types and outcomes, never an input or an event's content.
9. **At a cluster**, garam-bot-operator's ADR 0099 decides the connector's side:
   - the workspace namespace and the Pod provider's rights there;
   - every Bot's workspace credential;
   - the connector serving with garam off.

## Consequences

- **Rights.** A workspace's rights and credentials stay in one party at the place, the connector. Through interface 7, a bot's process can reach its own workspaces' token files and adapter, and nothing else of any workspace. What the network allows besides is the provider's: a Pod workspace's NetworkPolicy holds only where the cluster's network plugin enforces it, and a naru Machine's port is guarded by interface 2's AUTH-1 alone.
- **Standalone.** The connector serves every Bot, garam-owned or declared at the place, so it runs where garam is off too. The manager gives every Bot the workspace credential and the trust bundle.
- **Interface 3 gains `Spec.Labels` and `Capabilities.Labels`** in its release 0.3.0, ahead of the connector's side.
- **The Pod provider moves** from `internal/podworkspace` to `pkg/podworkspace`, as the naru Machine provider is under `pkg/`, so the operator module's connector links it.
- **Builds owed.** Interface 7 needs its reference server and its server suite, built with the connector's side. The callers' suites of interfaces 2 and 3 (G9, garam-bot#1216) run against `garam-bot run` and this interface's reference server.
- **A connector restart** costs each process up to 8 minutes of reading its events and writing its files, while its tasks run on in the adapter.
- **Every `Dial` passes through the connector.** One connector carries every bot's agent traffic at its place.
- **Before G3 and G6**, a bot outside the end-to-end suite opens no session, because it has no model token.
- **The rest of `spec.workspaces`.** Attachments by `ref`, `SharedRead` access, `Session` lifetime and every attachment after the first are refused by acquire with a reason until an issue builds them. So is a Bot's `permissions.egress`, which interface 7 does not resolve.
- **At a PC and a naru host**, the daemon serves interface 7, with a credential decided with the daemon, as interface 6's is (ADR 0039).

## Signs the decision was wrong

- **The connector is the bottleneck.** Agent traffic through it slows the bots' event reads, or its loss stops every bot's work for longer than its tasks can wait. That would show `Dial` belonged nearer the workspace.
- **Processes wait after their own restarts or after the connector's.** A restarted process is refused by its pin often enough to be seen, or a connector restart leaves processes without events for minutes regularly. That would show pinning or pre-start refusal costs more than the theft they close.
- **The cut set of calls grows back.** A driver needs exec, or reading a file, for its own work, which would show the set was too small.
- **The daemon cannot serve interface 7 unchanged**, which would show its shape is the cluster's.
