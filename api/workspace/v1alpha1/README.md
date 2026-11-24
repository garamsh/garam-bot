# Workspace provider v1alpha1

The normative specification of garam-bot's interface 3, the workspace provider, at version `v1alpha1`: the `Provider` a program at an execution place drives to make a workspace, run it, reach into it, snapshot it and delete it. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Workspaces and names
- Lifecycle
- Guest access
- Network
- Snapshots
- Errors
- Mapping onto naru's Machine API
- Mapping onto the other implementations
- Versioning and compatibility
- Open questions

## Scope and implementations

A workspace is where a bot's agent runs and keeps its files: interface 2's adapter runs inside it, and the bot's process runs outside it (ADR 0031). A workspace provider makes workspaces of one kind. The caller is the program that runs bots at an execution place: the cluster connector, the PC daemon or the naru-host connector (the bot system design, `garamsh/garam-bot#1204`). garam never calls a provider. A WorkspaceClass of interface 1 names its provider by `spec.provider`.

The interface is the Go interface `Provider` in this package, a driver API: an implementation is a Go package that the caller links, and it reaches its backend however that backend is reached. The types in this package are the schema, and `workspace.go` holds the rules every implementation applies (`ParseName`, `Spec.Check`, `SameSpec`).

| Implementation | Backend | Places | State |
|---|---|---|---|
| `workspacefake` | memory | the conformance suite | the reference implementation, built, passing the suite |
| naru Machine | naru's Machine API `machines/v1alpha1` | a naru host | first production implementation; to build |
| Pod | the Kubernetes API | a cluster | to build |
| container | a container engine's API: Docker or Podman, or Apple's container framework | a Linux PC, a Mac | to build |
| mvmd | mvmd's runtime API `v1alpha2` | a Linux PC | to build |

The first two implementations the suite runs against are `workspacefake` and the naru Machine provider. The suite drives each through `Provider` alone.

What this interface does not carry:

- **No secret.** `Spec` has no field for one. The only credentials in a workspace are the short-lived token files interface 2 names, which the caller writes with `WriteFile` and rewrites before they expire.
- **No image choice.** `Spec.Image` comes from the execution place's configuration. Interface 1 names no image (its IMG-1).
- **No placement.** A provider decides where a workspace runs inside its backend. The caller names a provider, never a host.

## Workspaces and names

- **NAME-1.** A workspace and a snapshot are named by the caller, with a DNS label of at most 63 characters (`ParseName`). An implementation MUST refuse any other name with `ErrInvalid`. Names are the caller's own: an implementation MUST answer a name another caller holds as `ErrNotFound`.
- **SPEC-1.** An implementation MUST refuse with `ErrInvalid` a spec that `Spec.Check` refuses: an image that is not `repository@sha256:<digest>`, a resource that is zero, port 0, or a malformed egress rule.
- A workspace has one data volume of `Resources.DiskBytes`, mounted at `DataPath`, `/workspace`. What survives a stop, a start, a move inside the backend and a restore is what is under it, and nothing else is promised to (DATA-1).

## Lifecycle

- **LIFE-1.** `Create` is idempotent by name: a name that holds a spec `SameSpec` takes as equal answers that workspace with `created` false, and a name that holds another spec MUST be refused with `ErrConflict`. A spec is never changed in place: a resize is a delete and a create.
- **LIFE-2.** A workspace `Create` made reaches `PhaseRunning` unless it fails, and `Get` and `List` answer it. `List` is ordered by name.
- **LIFE-3.** `Stop` and `Start` set the desired state and are idempotent, and the workspace reaches `PhaseStopped` and `PhaseRunning`. `Stop` gives the workspace `grace` to stop before it is ended.
- **LIFE-4.** `Delete` is idempotent. It deletes the workspace and its data volume and keeps its snapshots. Once the backend has let go of the workspace, `Get` MUST answer `ErrNotFound` and the name MUST take a new `Create`. Until then a `Create` of the name MAY answer `ErrDeleting`.
- **LIFE-5.** A call that names a workspace or a snapshot the caller does not have MUST answer `ErrNotFound`.
- The phases are `pending` (recorded and not yet running: being placed, started, restored or moved), `running`, `stopping`, `stopped`, `failed` and `unknown`. `Status.Reason` says why a workspace is pending, failed or unknown.
- **DATA-1.** What is written under `DataPath` MUST read back the same after a stop and a start.

## Guest access

- **RUN-1.** `Exec`, `ReadFile`, `WriteFile` and `Dial` reach a running workspace only. An implementation MUST answer each with `ErrNotRunning` for a workspace that is not running.
- **EXEC-1.** `Exec` runs `Command[0]` with the rest as arguments, not through a shell, with `Stdin` as its input, and returns its exit code, stdout and stderr whole. A command that exits non-zero is a result, not an error. A `Timeout` above zero bounds the run.
- **FILE-1.** `WriteFile` replaces the file at an absolute, clean path with its content, whole, and `ReadFile` returns it whole. An implementation MUST refuse another path with `ErrInvalid`.
- **FILE-2.** A read of a path that holds no file MUST answer `ErrFileNotFound`.
- **ISO-1.** A workspace MUST NOT see another workspace's files, whichever caller holds the other.
- **DIAL-1.** `Dial` opens a connection to one of the spec's `Ports` in the workspace, carried as the implementation reaches its backend. An implementation MUST refuse a port the spec does not name with `ErrInvalid`. Interface 2's adapter listens on such a port, and the caller speaks to it through `Dial`.

## Network

- **NET-1.** A workspace MUST NOT send anything that its spec's `Egress` rules do not allow. With no rule, it sends nothing out. An implementation that cannot enforce rules reports `Capabilities.Egress` false, and MUST then refuse a spec carrying any rule with `ErrUnsupported`, never run it with wider egress.
- What reaches a workspace is a `Dial` from its caller, and nothing else.
- The rules are prefixes, not host names: the caller resolves what a bot's `permissions.egress` names, the model gateway and the tool endpoints of interface 2, and writes their addresses as rules.

## Snapshots

- **SNAP-1.** `Snapshot` takes the data volume of one workspace, consistent when taken stopped and crash-consistent when taken running. It is idempotent by name: the same workspace answers that snapshot with `created` false, and a name holding a snapshot of another workspace MUST be refused with `ErrConflict`. A snapshot reaches `SnapshotReady` unless it fails.
- **SNAP-2.** A `Create` with `FromSnapshot` makes the data volume from the snapshot, which outlives the workspace it was taken of until `DeleteSnapshot`. A snapshot the caller does not have MUST be refused with `ErrSnapshotNotFound`.
- **SNAP-3.** An implementation that offers no snapshot reports `Capabilities.Snapshot` false, and MUST answer `Snapshot` and a `Create` with `FromSnapshot` with `ErrUnsupported`.
- A snapshot holds no memory. A WorkspaceClass's `idle.sleepAfter` is served by `Stop` and `Start`, not by a memory snapshot.

## Errors

Every refusal is one of the sentinels in `errors.go`, wrapped with `%w`, read with `errors.Is`. An implementation translates its backend's errors into them before it returns, and returns any other failure as an error none of them matches. A caller treats such an error as a failure of the backend and retries with backoff.

## Mapping onto naru's Machine API

The naru Machine provider is a client of `machines/v1alpha1` (`garamsh/naru` `lib/machineapi`, `docs/architecture/machines.md` §The API), at the naru host or cluster where the caller runs. It holds a `caller` certificate that it gets by registering once with a one-time token and a key it makes on site, and renews itself (`garamsh/naru#409`). One workspace is one Machine, named by the workspace's name.

| Provider | Machine API |
|---|---|
| `Create(name, spec)` | `PUT .../machines/{name}`, `state` running. 201 is created, 200 is the same spec, 409 `conflict` is `ErrConflict`, 409 `deleting` is `ErrDeleting` |
| `spec.Image` | `image`, the same digest reference |
| `spec.Resources.MilliCPU` | `cpu`, rounded up to whole CPUs |
| `spec.Resources.MemoryBytes` | `memoryBytes` |
| `spec.Resources.DiskBytes` | one volume `{name: workspace, sizeBytes, mountPath: /workspace}`. `rootDiskBytes` is the provider's configuration |
| `spec.Env` | `env`. A read answers each value as `<set>`, and the create's comparison is the server's, so the provider compares nothing itself |
| `spec.Ports` | `services`, one `tcp` service per port, named `p<port>` |
| `spec.Egress` | `networkPolicy.egress`, one rule each: `prefix`, `protocol` and `ports`. `Capabilities.Egress` is true |
| `spec.FromSnapshot` | `fromSnapshot.snapshot`. 422 `snapshot-not-found` is `ErrSnapshotNotFound` |
| `Get`, `List` | `GET .../machines/{name}`, `GET .../machines` |
| `Start`, `Stop(grace)` | `POST .../start`, `POST .../stop` with `graceSeconds` |
| `Delete` | `DELETE .../machines/{name}`, 202. Its volumes go, its snapshots stay, and the name is held until the volumes are gone |
| `Exec` | `POST .../machines/{name}/exec`: `command`, `stdin`, `timeoutSeconds` |
| `ReadFile`, `WriteFile` | `GET`, `PUT .../machines/{name}/files?path=` |
| `Dial(port)` | TLS to naru-proxy with the SNI `p<port>--<id>.<zone>`, the service's stable name (`address.name` with the service in front, naru ADR 0112), then plain TCP to the Machine |
| `Snapshot(name, of)` | `PUT .../snapshots/{name}` with `machine`. Phases map one to one |
| `GetSnapshot`, `DeleteSnapshot` | `GET`, `DELETE .../snapshots/{name}` |
| `Capabilities.Snapshot` | true: `capabilities.snapshot` always holds `volumes` |

| Machine phase | Workspace phase |
|---|---|
| `pending`, `starting`, `moving` | `pending` |
| `running` | `running` |
| `stopping`, `deleting` | `stopping` |
| `stopped` | `stopped` |
| `failed` | `failed` |
| `unknown`, and any phase the client reads as `unknown` | `unknown` |

| Machine API code | Provider error |
|---|---|
| `invalid` | `ErrInvalid` |
| `not-found`, `machine-not-found` | `ErrNotFound` |
| `snapshot-not-found` | `ErrSnapshotNotFound` |
| `conflict` | `ErrConflict` |
| `deleting` | `ErrDeleting` |
| `not-running` | `ErrNotRunning` |
| `file-not-found` | `ErrFileNotFound` |
| a capability's 409 | `ErrUnsupported` |
| `forbidden`, `internal`, `cursor-expired`, `snapshot-mismatch` and any other | a failure that matches no sentinel |

What the Machine API offers that this version does not use: aliases, the shape (`region`, `labels`, `gpu`) and the event log. The provider reads status by `GET` until the event log is used (Open questions).

## Mapping onto the other implementations

Each mapping is its implementation issue's to build, and this section is what the issue holds to.

| Provider | Pod | container | mvmd |
|---|---|---|---|
| One workspace is | a Pod and a PersistentVolumeClaim of `DiskBytes` at `/workspace` | a container and a named volume at `/workspace` | a Machine of mvmd's with a data disk at `/workspace` |
| `Create`, `Delete` | create, delete both | create, remove both | Create Machine and Create disk, Delete both |
| `Start`, `Stop` | `Stop` deletes the Pod and keeps the claim, `Start` makes the Pod again | start, stop | Start, Stop |
| `Exec` | the `exec` subresource | the engine's exec | Exec, then Read exec output to its end |
| `ReadFile`, `WriteFile` | an `exec` of the image's `cat` | the engine's archive copy | Read file, Write file |
| `Dial` | the `portforward` subresource | a port published on loopback | the Machine's address on the host's bridge, under an ingress rule |
| `Egress` | a NetworkPolicy of `ipBlock` rules | the engine cannot enforce rules: `Capabilities.Egress` false, and no network | `networkPolicy.egress` |
| `Snapshot` | a VolumeSnapshot, where the cluster serves the API | false | Snapshot disk |

## Versioning and compatibility

- **A release of `v1alpha1` may add** a method on a new interface the caller asserts for, an optional field of `Spec`, a field of `Capabilities`, a phase or a sentinel. Each leaves every implementation that passed the suite passing it, and every caller that built against an earlier release building.
- **A new version is required** to add a method to `Provider`, remove or rename anything, make a field required, tighten a rule of `Spec.Check`, or change what an operation means, its idempotency or the state it leaves after a failure.
- **VER-1.** Each release has a number, `SpecRelease` in this package, from `0.1.0`, and the `api/` module's tag moves with every release.
- **VER-2.** A caller MUST read a phase it does not know as `unknown`, and a snapshot phase it does not know as `SnapshotUnknown`. This is what makes adding a phase a compatible release.
- **A deprecated element** is marked so here and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the suite.

## Open questions

- **Events.** The Machine API, the Kubernetes API and the container engines each offer a stream of changes. This version reads by `Get`. A `Watcher` interface the caller asserts for is an addition.
- **Exec streams.** `Exec` returns its output whole, as the Machine API's does. A streaming exec is an addition, and interface 2 does not need one: the adapter is reached through `Dial`.
- **A file's mode.** `WriteFile` takes no mode, as the Machine API's file call takes none. Whether the token files of interface 2 need one that no other process in the workspace can read waits for the adapters.
- **What `Timeout` leaves.** Whether an `Exec` past its timeout is a result or an error is not decided, because the Machine API does not state it.
- **The Pod's `ReadFile`.** An `exec` of `cat` needs the image to carry it. A sidecar of garam-bot's own is the alternative.
- **A Mac's container engine.** Docker Desktop, Podman and Apple's container framework each run a Linux VM. Which ones the container implementation supports is its issue's.
