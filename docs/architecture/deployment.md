# Deployment

Governs what has to be true to run the workspace as a container: what it binds, what it mounts, what kills it at boot, what an orchestrator must allow on shutdown, and how its images are published. The architecture behind it is `environment.md` and `security.md`; this file states the operator-facing consequences and does not restate them. The runtime container that runs one Bot is `runtime.md`'s. Rationale: ADR 0003, ADR 0008, ADR 0010, ADR 0020, ADR 0033, ADR 0040, ADR 0041.

The target direction replaces the execution boundary described here (ADR 0031). This document describes the shipped shape until P2 of ADR 0031 lands.

## Contents
- The workspace container
- Listener and the bind gate
- Volumes and ownership
- Configuration
- Capabilities
- Startup: what kills the container and what degrades it
- Health and readiness
- Shutdown
- Images and tags
- Open questions

## The workspace container

One binary, `garam-bot`, built from `build/workspace.Dockerfile`, serves the workspace as `garam-bot workspace`.

| | workspace container |
|---|---|
| Command | `garam-bot workspace` |
| Image built from | `build/workspace.Dockerfile` |
| Runs as | root |
| Shell in the image | yes |
| Serves | the gRPC data plane (Read, Write, List, Exec) |
| Repository example | `configs/workspace.yaml` |

**The *Runs as* row is what the image sets, not what the pod asserts.** It is the Dockerfile's `USER`, and an orchestrator admitting the pod reads no Dockerfile. What a manifest has to assert, and which Pod Security Standard each shape can reach, is §Capabilities.

**The base image is not named here.** It is pinned by digest in the Dockerfile, which is the only copy (§Images and tags).

**The workspace container needs root in the privileged shape**, because it drops Exec children to another uid. The unprivileged shape runs it as a non-root uid that is also its `exec-uid` (§Capabilities).

The workspace is operated by the `garam-bot workspace` operation subcommands and by `garam-bot workspace mcp`, both run in a container that can reach its listener (ADR 0027, ADR 0028, `environment.md`).

## Listener and the bind gate

| Listener | Default | Setting | Environment |
|---|---|---|---|
| Workspace gRPC | `127.0.0.1:8081` | `addr` | `GARAM_BOT_ADDR` |

The operation subcommands and `garam-bot workspace mcp` find it through `--workspace-addr`, then `GARAM_BOT_WORKSPACE_ADDR`, then `127.0.0.1:8081`, and read no config file.

**The bind gate.** The workspace refuses to serve beyond localhost: its gRPC listener is unauthenticated until contract authentication exists, so a non-loopback `addr` fails startup and the container crash-loops, with no TLS escape hatch. **The workspace can never be fronted by a Service.** It is a same-pod sidecar reached over localhost, which is what makes the shared network namespace part of the deployment rather than a convenience.

## Volumes and ownership

| What | Setting | Default | Written by |
|---|---|---|---|
| Workspace directory | `workspace` | `data/workspace` | the workspace container |

- The workspace directory is created on first use. When the workspace runs privileged it chowns that directory to `exec-uid`, keeps the directory's write bit for its own group, and gives every file it writes over the contract to the same uid, so contract writes and Exec children share one owner (ADR 0008). An unprivileged workspace owns the directory itself.
- **A directory a host user created beforehand carries that user's group**, so the privileged workspace process is then "other" and every write over the contract fails with `permission denied`. Let the container create it, or hand it over as `deploy/local/README.md` describes.
- **An Exec child can reach only what its uid and its mounts reach** (ADR 0008). A volume that holds anything else, such as a caller's store, belongs in a container that does not mount the workspace's, because a distinct uid separates contents and not names.

| Shape | Holds the invariant |
|---|---|
| The workspace container mounts the workspace directory alone; it runs privileged and drops Exec to an `exec-uid` that is not its own uid | yes — the privileged shape |
| The workspace container mounts the workspace directory alone; it runs unprivileged and `exec-uid` is its own uid | yes — the unprivileged shape, by the mount alone, given its two host requirements (§Capabilities). What it leaves unseparated is the workspace process itself: an Exec child can signal the workspace server and stop the data plane until the container restarts (ADR 0008) |
| A volume holding another party's owner-only files mounted into the workspace container as well; privileged workspace, distinct `exec-uid` | for contents only: the child can neither write nor read them, but it lists their directory and so sees the names |
| The same volume, with `exec-uid` equal to the uid that owns those files | no: Exec reads and writes them |
| An unprivileged workspace whose `exec-uid` is its own uid, with another party's volume also mounted into it | not supported: that shape holds the invariant by the mount and by nothing else |

**What the workload requires of the workspace volume is survival: it must outlive a reschedule.** That is a durability property and not a capacity one, and it is the failure that looks like success — the container runs, the data persists, and the loss happens when it moves. Which storage satisfies it is the orchestrator's. No storage class, default, or access mode is named here: this repository cannot read a cluster, so a claim about one would go stale with nobody in a position to notice.

**Owner-only config.** The workspace's config file is mode-checked by the loader (`internal/config/`): `0600` and `0400` pass, `0644` fails and the process exits. Kubernetes projects Secret and ConfigMap volumes at `0644` by default, so a mounted config needs an explicit `defaultMode`.

## Configuration

Settings resolve in one precedence: **flags > `GARAM_BOT_*` environment > config file > defaults.** The environment name is the setting with dashes and dots replaced by underscores, so `exec-uid` is `GARAM_BOT_EXEC_UID`.

Config files resolve in order: explicit `--config <path>`, then `./configs/workspace.yaml`, then `os.UserConfigDir()/garam-bot/workspace.yaml` (`internal/config/`). A missing automatic file falls through to defaults; a missing explicit path fails startup. From the repository root, run `chmod 0600 configs/workspace.yaml` before starting: Git does not preserve owner-only modes. Relative settings resolve from the process working directory. The local Compose deployment mounts its own config and selects it explicitly (`deploy/local/docker-compose.yml`).

**A config-file key no setting reads is reported, not refused.** The workspace warns once per unread key, naming the key and the file, and starts anyway — so a renamed key does not reject an operator's whole file. The warning reaches a log inside the container and nothing else.

**`--strict-config` refuses what the default reports** (#932). Every key that would be warned about fails startup instead, and the error names the keys and the file. It is a flag only: it is read off the command line rather than through the settings layers, so neither `GARAM_BOT_STRICT_CONFIG` nor a `strict-config` key in the file it checks turns it on or off.

Settings an operator meets:

| Setting | Default | Meaning |
|---|---|---|
| `addr` | `127.0.0.1:8081` | the listener (§Listener and the bind gate) |
| `workspace` | `data/workspace` | the workspace directory |
| `exec-uid` | `1001` | the uid Exec children run as; in the unprivileged shape, the workspace's own |
| `exec-timeout` | `30s` | the per-call ceiling for Exec |

**Choose `exec-uid` as a uid nothing else on the node runs as.** Containers share the node's uid space and `RLIMIT_NPROC` counts every process of the real uid, so a shared account spends the child's 64-process budget on processes that are not the child's. The code default is `1001`, which is the login user on many hosts; the sample deployment overrides it to `10001` for exactly this reason (`deploy/local/docker-compose.yml`).

**The workspace holds no secret and its Exec children get a scrubbed environment** (ADR 0003): nothing in the workspace's own environment reaches a child.

**Every outbound destination is the operator's choice and none is the caller's.** The workspace makes no outbound call. Egress control for the container is the pod's network policy, written from the operator's own configuration, not from a list this repository holds; an Exec child's own network is an empty namespace (ADR 0010).

## Capabilities

In the privileged shape the workspace container needs five, and they divide on a line that decides the pod's Pod Security Standard:

| Capability | For | PSS `baseline` |
|---|---|---|
| `SETUID`, `SETGID` | dropping the Exec child to `exec-uid` | allowed |
| `CHOWN` | handing the workspace directory to `exec-uid` | allowed |
| `KILL` | signalling that child on expiry — the drop leaves it owning neither of the workspace's uids | allowed |
| `SYS_ADMIN` | `unshare(CLONE_NEWNET)` — the fresh, empty network namespace an Exec child gets per call | **not allowed** |

**The privileged shape targets `baseline` and cannot reach `restricted`; the unprivileged shape reaches `restricted`.** `restricted` is reached only where every container in the pod runs non-root, and the privileged workspace runs as root to drop Exec children to another uid (§The workspace container). The `SYS_ADMIN` row is what reaching `baseline` costs. The unprivileged shape adds no capability and runs non-root; what it gives up for `restricted` is the uid boundary between an Exec child and the workspace process (ADR 0008): the child reads what the workspace's uid can, and can signal the workspace server, stopping the data plane until the container restarts. What it asks of the node instead is two host requirements, below.

**`SYS_ADMIN` is netns creation, and it cannot be moved to an initContainer.** The unshare happens inside each Exec child immediately before the credential drop, not once at startup (`internal/workspace/shell/`), so the workspace container holds the capability for its whole life.

**Dropping it does not fail the container.** Without `SYS_ADMIN` the workspace starts normally and every network-isolated Exec call fails closed with `agent_uid unavailable`. The only signal is a WARN at startup — `CAP_SYS_ADMIN missing; netns-isolated children will report agent_uid unavailable`. Nothing in the pod's status reports it, so an operator who drops the capability to reach `baseline` gets a running workspace that cannot execute anything. That is the fail-closed posture ADR 0008 and ADR 0010 choose deliberately: a capability that is unavailable is better than one silently upgraded. (`agent_uid unavailable` is the error code's literal text; it predates the removal of the agent and is unchanged.)

**`KILL` is the deadline, and it is the one that looks removable.** It grants nothing over the node: the container holds its own PID namespace, so the only processes it can reach are the workspace and the children it started. What it buys is the ability to signal them at all. `kill(2)` permits a signal only when the sender's real or effective uid matches the target's real or saved-set uid, or the sender holds `CAP_KILL`, and the drop leaves the child owning neither of the workspace's uids. Until #612 the shipped set omitted it, and the consequence was invisible: the SIGKILL on expiry did nothing and a 100ms deadline took as long as the command did. The unprivileged shape never needs it, because there the child owns the workspace's own uid.

**Dropping `KILL` now fails every Exec, by choice.** A privileged workspace that cannot signal its child refuses the call with `agent_uid unavailable` and a message naming the capability, rather than running a command whose deadline would pass unenforced. It warns at startup too — `CAP_KILL missing; every Exec will report agent_uid unavailable`.

**The unprivileged shape needs a host rather than a capability** (ADR 0008, ADR 0010). Its workspace container runs as a non-root uid with no capability added, and its manifest asserts that: `runAsNonRoot: true`, a numeric `runAsUser`, `exec-uid` (`GARAM_BOT_EXEC_UID`) set to that same uid, `capabilities.drop: [ALL]`, and a workspace volume that uid can write. Under Docker standalone the same is `user:` and no `cap_add`. An Exec child that asks for `NoNetwork` — every `garam-bot workspace exec` call does — clones `CLONE_NEWUSER | CLONE_NEWNET` with the uid mapped to itself (`internal/workspace/shell/process_linux.go`), and lands in a fresh, empty network namespace, as the privileged shape's child does. The pod's network and a NetworkPolicy, or a Docker container's network mode, do not stand in for that namespace, and ADR 0010 records why it is rejected. Beside the volume requirement of §Volumes and ownership, the shape requires:

- **No process namespace shared with another container** — Kubernetes `shareProcessNamespace` unset, Docker `pid` not joined to another's. A child running as the workspace's uid would otherwise see that container's processes.
- **Host requirement: `user.max_user_namespaces` above `0` on the node.** A pod's security context does not set it; whoever builds the node does. Read it in the workspace container — `kubectl exec <pod> -c workspace -- cat /proc/sys/user/max_user_namespaces` — not on the node. The limit is held per user namespace, so the in-container read answers for the namespace the workspace runs in whether or not the pod keeps the default `hostUsers`, while a read on the node is right only in the default case.
- **Host requirement: a seccomp profile admitting `unshare` and `clone` with `CLONE_NEWUSER|CLONE_NEWNET`.** `RuntimeDefault` does not: under it, with the sysctl at `0`, each of those flags returned `EPERM` at syscall entry, before the limit is consulted (gitops, 2026-10-10; ADR 0008 records the measurement and its limit). `restricted` admits a `RuntimeDefault` or a `Localhost` profile, so there the requirement is a `Localhost` profile installed on every node that runs the workspace, named by the **workspace container's** `securityContext.seccompProfile`, while the pod and any other container stay on `RuntimeDefault`. Exec runs only in the workspace container, and a pod-level `Localhost` profile would loosen every other container too; a container-level `seccompProfile` overrides the pod's, and `restricted` checks each container's. Under Docker standalone, likewise, only the workspace container gets `--security-opt seccomp=` with a profile admitting the same. **Meeting the sysctl alone leaves every `NoNetwork` Exec refused.** Both are infra artifacts, not anything this repository ships.
- **Both host requirements checked rather than assumed** — where the workspace runs, as the account it runs as, under the security context it runs under:
  - **`unshare --user --net true` succeeds** with no capabilities added. Reading the sysctl answers for the kernel only, while the seccomp profile or an LSM policy can refuse a clone the kernel would permit, so what settles the question is making the call rather than reading a value.
  - **The shell package's single-account netns cases run rather than skip.** They make the call the workspace makes, and the helper they run through turns the fail-closed sentinel into a skip (`internal/workspace/shell/netns_linux_test.go`), so on a host that refuses the clone they skip and `make ci` still passes. With `GARAM_BOT_REQUIRE_USERNS` set they fail instead, naming the refusal, as the `ci` job in `.github/workflows/ci.yml` sets it so a runner image that refuses the clone cannot leave that job green (#1077); `GARAM_BOT_REQUIRE_USERNS=1 make test` is the same check on a host of your own. A green pipeline is not evidence about this path; those cases running is. **A skip does not by itself mean the host refused the clone.** `TestProcessExecutor_SingleAccountNoNetworkBlocksLoopback` and `TestProcessExecutor_SingleAccountLoopbackDownInNamespace` also skip when `python3` is not on the PATH, which is the PATH an Exec child inherits (`requirePython3`, and `childenv.go` in the same package), and the skip message says which: `python3 not on PATH` is the probe's own prerequisite, `this host cannot provide what an isolated call needs` is the clone. Read the message before asking infra for a change the host may already have.

**Either host requirement unmet, every `NoNetwork` call is refused, and the workspace says so at startup.** The clone's refusal becomes the same `agent_uid unavailable` every undeliverable boundary reports, and the message names the errno and the host requirement it points to, because the fix differs: `EPERM` is a seccomp profile or an LSM refusing the clone, `ENOSPC` (`EUSERS` on older kernels) is `user.max_user_namespaces`, and `EINVAL` is the kernel rejecting the namespace flags, for example one built without user or network namespaces (#1057). So the call is refused rather than served with the pod's network (ADR 0010). Because the operation subcommands ask for `NoNetwork` on every `exec`, that is every caller-influenced Exec, while Read, Write and List keep working. ADR 0010 requires the workspace to state at startup whether it can serve `NoNetwork`, and it does in either shape (`NewProcessExecutor` in `internal/workspace/shell/process_linux.go`). Where it is root, the `SYS_ADMIN` probe warns. In the unprivileged shape it makes one `NoNetwork` call at startup, through the same path every Exec takes, and when that call fails logs one WARN, `shell: NoNetwork probe failed; …`, carrying the error every such call will report. Because it makes the call rather than reading a value, it catches a seccomp or LSM denial as well as the sysctl. It refuses nothing: the workspace starts and serves Read, Write and List.

## Startup: what kills the container and what degrades it

The list below is the set an operator can trigger from a pod spec. It is a selection, not the whole of it: the code carries further startup error sites that no deployment choice reaches — a failed `setenv`, an unresolvable executable path, errors reachable only on a non-Linux build. If the container fails at boot with a message not below, it is one of those.

Every fatal path exits **1** and prints `Error: <message>`; nothing distinguishes them by exit code, so the message is the only diagnosis.

**Kills the process at startup:**

| Cause | Message shape |
|---|---|
| Config file group- or other-readable | `config file "…" is group- or other-accessible (mode 0644); it must be owner-only` |
| `--config` names a missing or malformed file | viper's parse error |
| `--strict-config` and a key nothing reads | an error naming the keys and the file |
| Non-loopback `addr` | `addr "0.0.0.0:8081" is not a loopback bind: the gRPC listener is unauthenticated and serves loopback only` |
| Workspace directory cannot be created, chowned or chmodded | `create workspace root "…": …` / `chown "…" to uid 1001: …` |
| Listen address already bound or rejected | `listen tcp …: bind: address already in use` |

**Starts, and degrades:**

| Cause | Effect |
|---|---|
| `SYS_ADMIN` absent | Network-isolated Exec fails closed. WARN at startup, nothing afterwards |
| The host refuses the unprivileged user namespace — the sysctl at `0`, or a seccomp profile such as `RuntimeDefault` refusing the clone — and the workspace runs unprivileged | Network-isolated Exec fails closed with `agent_uid unavailable` — every `exec` call, since each asks for isolation. WARN at startup: `shell: NoNetwork probe failed; …`, carrying that error (§Capabilities) |
| `KILL` absent, and `exec-uid` is not the workspace's own uid | **Every Exec fails closed** with `agent_uid unavailable: CAP_KILL is absent, …`. WARN at startup |
| `exec-uid` unreachable — the workspace runs unprivileged and `exec-uid` is not its own uid | **Every Exec fails closed** with `agent_uid unavailable`. No startup check, no warning |
| The unprivileged shape — the workspace runs unprivileged and `exec-uid` is its own uid | Exec runs, as the workspace's own account. INFO at startup: `unprivileged shape: …`, naming the shape's requirements. The workspace cannot see whether another party's volume is mounted into it, so it does not refuse the shape whatever the volumes |
| `exec-timeout: 0` | Every Exec without a per-call timeout fails: `shell: timeout is required`. No startup check |

Three traps in that second table are worth naming separately, because they produce a healthy-looking container that cannot work: **an unreachable `exec-uid`**, **a missing `SYS_ADMIN`**, and **a host that refuses the unprivileged user namespace**. Each leaves exec dead with nothing in pod status to show it. The fix for the first two is the privileged shape's full capability set (§Capabilities) or the unprivileged shape: `exec-uid` equal to the container's `runAsUser`. The fix for the third is both host requirements of §Capabilities — the sysctl and a seccomp profile admitting the clone; the sysctl alone fixes nothing under `RuntimeDefault` — or the privileged shape.

**A stalled workspace is refused by its caller, and `exec-timeout` still decides how long a command runs.** `exec-timeout` bounds the command the workspace runs, not the RPC carrying it, so a workspace that accepts the connection and then answers nothing never reaches it. Each operation therefore sets its own deadline on the call: Read, Write and List wait what a pod-local filesystem call needs, and `exec` waits the per-call timeout it was handed plus a margin for the workspace's dispatch (`cmd/garam-bot/workspace_client.go`). **A call naming no timeout is bounded against the stall alone, at one hour**, because the ceiling is then `exec-timeout`'s and the CLI cannot read it; an `exec-timeout` above an hour ends such a call in exit 3 (`environment.md` §Workspace CLI contract).

## Health and readiness

The workspace registers no gRPC health service and serves no HTTP. **`workspace listening` is logged after the bind**, so its presence in the log means the listener is up. A probe that needs more makes one cheap call on the listener, such as `garam-bot workspace list .` run in the container, and reads exit 0 as ready and exit 3 as no answer.

## Shutdown

The workspace traps `SIGTERM` and `SIGINT` and calls `GracefulStop`, which waits for in-flight RPCs with **no bound**. In practice an in-flight Exec is bounded by `exec-timeout` (default 30s). Exec children carry `Pdeathsig`, so they die with the workspace process. No total shutdown deadline exists as a flag, an environment variable, or a config key: the orchestrator's grace period is the only total bound, so derive `terminationGracePeriodSeconds` from your own `exec-timeout` rather than copying a number from here.

## Images and tags

Registry: **ECR, `ap-northeast-2`, account `486152169996`.** Each image has one repository, named `<publishing repository>/<image>` (garamsh/aurora ADR 0033, "A repository in `image_publishers` is named `<publishing repository>/<image>`", at `90d9ba9921f27eccb2f517f487cbb74ad412e0bd`), so this project's images are in `garam-bot/<image>` (ADR 0041). Each holds its image's releases and development builds alike, and the tag pattern tells them apart (ADR 0020):

- **Release.** A `v`-prefixed git tag triggers the release workflow, which strips the `v` and pushes the resulting semver (e.g., `0.1.0`). A semver tag is pushed by the release workflow and by nothing else.
- **Development.** A contributor builds and pushes by hand to the same repositories, with a tag of the build's commit short SHA (`git rev-parse --short=12`, as `837b80afe272`), never a semver. A semver holds two dots and a short SHA none, so the two patterns cannot meet.

| Image | Local name | Built from | Repository | Role |
|---|---|---|---|---|
| workspace | `garam-bot-workspace:<commit>` | `build/workspace.Dockerfile` | `garam-bot/workspace` | the workspace container (this document) |
| runtime | `garam-bot-runtime:<commit>` | `build/runtime.Dockerfile` | `garam-bot/runtime` | the container that runs one Bot (ADR 0033, `runtime.md`) |
| workspacefile | `garam-bot-workspacefile:<commit>` | `build/workspacefile.Dockerfile` | `garam-bot/workspacefile` | the Pod workspace provider's init container (ADR 0034, `cmd/workspacefile`) |
| claude-code | `garam-bot-claude-code:<commit>` | `build/claude-code.Dockerfile` | `garam-bot/claude-code` | the claude-code adapter (ADR 0035, `cmd/claude-adapter`) |

`make build-images` builds them and refuses an image whose `Config.User` is not numeric, so the kubelet can check `runAsNonRoot` against it. The repositories for runtime and workspacefile and the release role's push grant on them are the account owner's (garamsh/aurora#866, garamsh/aurora#1000). No development build of runtime or workspacefile is pushed by hand. The conformance image of interface 3 (`build/conformance.Dockerfile`) is built by `make test-podworkspace` for that run only, and `make test-claude-code` builds the claude-code image for that run only; no release carries either.

**The sherlock agent image no longer exists** (ADR 0040). The repositories `garam-bot/agent` and the earlier `sherlock/agent` and `garam/sherlock-agent` hold what they held and receive nothing more. Releases `0.1.0` through `0.9.0` were published as `garam/sherlock-agent` and `garam/sherlock-workspace`; their digests, the role grants and the CloudTrail readings that backed them are in this file's git history (`git log -p -- docs/architecture/deployment.md`), and not restated here.

**A release chooses its version against that history, not against the registry.** A tag names one commit and never moves. Immutability enforces that only within one repository's life: deleting and recreating a repository empties what it can refuse, so a name the registry accepts may still have named another commit before. A release therefore takes a version no earlier release used; `0.9.0` was the last in the earlier repositories, and `0.1.0` is the one name that named two commits.

**The claude-code adapter's image** (ADR 0035, `build/claude-code.Dockerfile`, `cmd/claude-adapter`) is built by `make build-images` as `garam-bot-claude-code:<commit>`, which also refuses it unless its `Config.User` is numeric. It carries the adapter and the Claude Code CLI at a pinned version on a Node base pinned by digest. It has a fifth repository, **`garam-bot/claude-code`**, under the same naming and tag rules. The release workflow publishes it as one more entry in `IMAGES`, last in the push order, so the existing-tag check, the push and the read-back cover it with no branch of its own. `make test-claude-code` builds it and runs interface 2's suite against it for that run only.

**Pushing a development build by hand.** In order, from a checkout at the commit to publish:

1. `make build-images` with no `REVISION=`, so the dirty-tree comparison runs and refuses a tree that differs from the commit (**What the local build produces**, below). It writes `garam-bot-workspace:<commit>`, where `<commit>` is `git rev-parse HEAD`.
2. Log docker in to `486152169996.dkr.ecr.ap-northeast-2.amazonaws.com` with `aws ecr get-login-password --region ap-northeast-2`, under a credential of your own that may push to the repository. The release workflow's role is not one: its trust admits a `v*` tag ref of this repository and nothing else. Who may hold such a credential is the account owner's (garamsh/aurora) to grant.
3. `docker tag` the local `garam-bot-workspace:<commit>` as `486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/garam-bot/workspace:<short>`, where `<short>` is `git rev-parse --short=12 HEAD`, then `docker push` it.
4. Pin it by the digest its push reports. A short SHA already taken is refused on the second push; the repair is a new commit, never a retry under the same tag. A development tag may expire under the account's policy, so a pin on one is temporary; only a release tag is pinned to stay.

**What this project requires of every repository it publishes to, and what it does not** (ADR 0020). Stated as requirements rather than as settings, because nothing here can read that account and a description of it goes stale with nobody able to notice:

- **Create every one of them `IMMUTABLE`, with no tag-mutability exclusion, and never make any of them mutable.** This project pushes no moving tag, so an exclusion buys it nothing; and an exclusion filter is a repository setting that can be changed later, so it reopens the retroactive reach that follows for every tag it comes to match. The setting belongs to the repository rather than to a tag and applies retroactively, and ECR records nothing about the policy a tag was pushed under, so a repository that is ever mutable leaves every tag it holds a label rather than an identifier — the ones pushed while it was immutable included.
- **Push no moving tag.** A tag names one commit and never moves, for a release and a development build alike; there is no `dev` tag and no `latest`. A release tag (semver) and a development tag (`<commit-short-sha>`) go to the same `garam-bot/<image>`, told apart by pattern, and either is taken once and refused on a second push.
- **Neither a lifecycle policy nor image scanning is required.** This project depends on neither. Expiry is the wrong behaviour for the production tags (semver), which hold released versions. Either control, run for that account's own hygiene, is its to decide — not this project's to require, and not this document's to describe.

**A consumer references an image by digest and sets `imagePullPolicy: Always`** (ADR 0020). The digest, because a tag's stability rests on a repository setting a consumer cannot check from outside. `Always`, because an image a reference names can be deleted from the repository holding it, so the failure to be loud about is a reference that stops resolving — which the default `IfNotPresent` turns into a per-node stale-cache fault that presents as the node's.

**Base images are pinned by digest.** Every `FROM` line in `build/` that names an image names `tag@sha256:...`, which is what `docs/convention/ci.md` §Verify a pinned dependency before adopting it requires of a container image; the tag stays for readability and the digest is what resolves. Two builds of one commit therefore pull the same base bytes. What moves those digests is `.github/dependabot.yml`, and it covers less than the whole job — see §Open questions.

**What the local build produces.** `make build-images` tags each image with the same revision it labels it with, never `:latest`, so an image cannot be tagged one commit and labelled another (#693). Where that revision is the git default, the build compares what docker reads against it first — the build context, the Dockerfiles it is handed, and the single ignore file that decides what the context holds — and refuses, naming the paths, where they differ; a build that passes its own `REVISION=` is stating its own claim and is not compared (#696, #698). The `docker` job in CI passes one on every run, so the comparison is a contributor's guard rather than a pipeline's. It also builds with buildx's default attestations off, so each image is one manifest rather than an index over one: an attestation is carried as a manifest of its own, and a digest naming the index that holds both is not a digest naming the image (ADR 0020). Whether the default would have applied depends on the host's image store, so the release reads the pushed object back rather than trusting the build.

**The release workflow.** `.github/workflows/release.yml` is the release path (ADR 0041). It triggers on a pushed tag and on nothing else — no branch, no pull request — refuses a tag that is not `vX.Y.Z` before it reaches AWS, builds every image it publishes through `make build-images`, and only then pushes them. All are built before any is pushed, because a version whose later image failed after an earlier one was pushed cannot be repaired under an immutable name, only abandoned. It reaches the registry as `arn:aws:iam::486152169996:role/github-actions-garam-bot-push`, assumed by OIDC, which garamsh/aurora#866 created, trusting this repository's `v*` and `operator/v*` tags and granting `garam-bot/*` (ADR 0041). The PM relayed its ARN and no one here has read it back.

- **Before any push,** it asks each repository the release publishes to for the version's tag and goes on only on a "no such tag" answer, `ImageTagDoesNotExist` or `ImageNotFound`. A missing repository, a refused read, or a version already taken stops the run with nothing published.
- **The order.** That read does not prove the push grant, so the least proven repository is pushed first: a refused push there stops the run before another repository takes the version.
- **After a failed push,** the read-back still runs and records the digest of each image that was pushed. It then reads each pushed reference back from the registry and reports the digest, so what a consumer pins is what the registry holds rather than what a local rebuild would produce.

**What a role that publishes here needs.** Seven actions:

- `ecr:BatchCheckLayerAvailability`, `ecr:InitiateLayerUpload`, `ecr:UploadLayerPart` and `ecr:CompleteLayerUpload`, on each repository it publishes to — the layer upload.
- `ecr:BatchGetImage`, on the same repositories. It is documented as a pull action rather than a push one, and buildx calls it before writing a manifest, so a set assembled from a documented push list is short by exactly this one.
- `ecr:PutImage`, on the same repositories, and `ecr:GetAuthorizationToken`, which takes no resource and is granted on `*`.

**A missing `BatchGetImage` fails late, and no review of the policy catches it.** The push is denied at `PutImage`, after every layer has uploaded, and a reviewer reading that policy against a documented push list finds nothing missing. Only an execution separates an unexercised role's two halves: until something assumes it, a trust policy that admits nobody and a grant that permits nothing look the same from outside. The surface moves and nothing here watches it, so confirm the set against a real push's CloudTrail record before writing a policy from this list.

**Every reversible step comes before the irreversible one.** A git tag can be deleted and re-pushed; a tag in an `IMMUTABLE` repository cannot, so the push is the only step of a release that consumes a permanent name. Everything that can fail belongs before it — the version guard, the build of every image, the role assumption, the registry login — and a failure in any of them costs a re-run and nothing else. The read-back is the exception and cannot be otherwise, because there is nothing to read before the push: a failure there says the registry does not hold what the run meant to publish, never that nothing was published.

**Nothing creates the tag.** The workflow reads a tag that was pushed by hand and creates none, and no other automation in this repository creates one either. `make build-images` builds locally and pushes nothing, and the `docker` job in `.github/workflows/ci.yml` builds the images as a smoke check and pushes nothing.

**When two images disagree, the system accepts them.** Nothing refuses and nothing warns. No revision reaches a running process: `REVISION` becomes the image tag and the `org.opencontainers.image.revision` label and is read by nothing the binary runs. Agreement is a property of how the images are published rather than something checked at run time: one `make build-images` invocation builds them from one commit, and one release run pushes them under one version.

## Open questions

- **What moves a base-image digest when the tag does not.** A digest receives no security patch; a tag does, and pinning gave that up deliberately. `.github/dependabot.yml` raises a pull request when a base image's tag moves to a new version, which is the covered half. The other half is not covered: Dependabot's docker ecosystem offers version updates and no security-update channel, and a digest refresh under an *unchanged* tag is conditional rather than promised. So a base republished with a CVE fix under the same tag may raise nothing, and the fallback is re-resolving the digest by hand. Determined on 2026-08-17 from GitHub's supported-ecosystems table and `dependabot-core`'s `docker/lib/dependabot/docker/update_checker.rb`; re-check it before relying on the covered half.
- **Caller-to-workspace authentication.** The workspace's gRPC listener ships pod-local and unauthenticated, which is what forces the loopback-only rule above (`security.md`, `environment.md`).
- **Whether the release role's grant on `garam-bot/*` has been exercised.** The role was relayed and not read back, and no release has assumed it (§Images and tags). Until one does, the push grant is unproven.
