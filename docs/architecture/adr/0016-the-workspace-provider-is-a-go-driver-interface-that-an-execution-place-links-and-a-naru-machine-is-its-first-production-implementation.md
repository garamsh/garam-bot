# ADR 0016: The workspace provider is a Go driver interface that an execution place links, and a naru Machine is its first production implementation

> Status: accepted
> Date: 2026-11-24

## Context

- ADR 0013 names interface 3, the workspace provider, and `garamsh/garam-bot#1193` lists its operations: create, attach, exec, files, snapshot and delete. `garamsh/garam-bot#1204` carries it forward under the direction the owner approved on 2026-11-24 (`garamsh/discussions` discussion 6, comment 19856107): one workspace interface, implemented by a naru Machine or a Pod in a cluster, by a container or mvmd on a Linux PC, and by a container on a Mac.
- The caller is the program that runs bots at an execution place: the cluster connector, the PC daemon or the naru-host connector. garam never dials into a place (the same comment), so garam calls no provider. naru's Machine API had named garam's Workspace as its first caller, and `garamsh/naru#409` and `garamsh/naru#378` move that caller to garam-bot's provider at the place.
- The backends already have interfaces of their own: naru's `machines/v1alpha1` (HTTPS with mutual TLS, `garamsh/naru` `lib/machineapi`), mvmd's runtime API `v1alpha2` (gRPC on a Unix socket), the Kubernetes API, and the container engines' APIs. A provider is an adapter from one of them to what a bot's process needs.
- `docs/convention/interfaces.md` names "a plugin or driver API" as a boundary its rules bind.

## Decision

1. **Interface 3 is the Go interface `Provider` in `api/workspace/v1alpha1/`,** a driver API that the caller links. Each implementation reaches its backend over that backend's own interface. Its operations are create, get, list, start, stop, delete, exec, read and write a file, dial a port, and snapshot, as `api/workspace/v1alpha1/README.md` states them. Attach is `Dial`: the caller reaches the agent's adapter in the workspace through a connection the provider carries.
2. **A workspace's state is one data volume at `/workspace`.** Only what is under it is promised to survive a stop, a move or a restore, and a snapshot holds it and no memory, as naru's Machine snapshot does.
3. **A workspace sends nothing out but what its egress rules allow,** and a provider that cannot enforce rules refuses them rather than run the workspace wider.
4. **The reference implementation is `workspacefake`, in memory, and the first production implementation is the naru Machine provider.** The specification maps every operation, phase and error onto `machines/v1alpha1`. A spec's fields reach the Machine API as written, so naru's create comparison is the interface's, and the provider follows naru's event log for status rather than polling. The Pod provider is the second (`internal/podworkspace`), and the container and mvmd providers follow, each its own issue.
5. **`Dial` carries a connection and does not authenticate it.** On naru it goes through naru-proxy, which naru#378 builds and which serves a service to whoever reaches it, so the listener behind a port authenticates every connection itself (interface 2's AUTH-1).
6. **On a Pod the file calls run garam-bot's own helper, not the image's tools.** A workspace's image is the place's, and an `exec` of its `cat` would make every image carry one and make `WriteFile` a shell redirect. An init container copies a small static binary (`cmd/workspacefile`, published by digest like the other images) into a read-only mount of the workspace container, and `ReadFile` and `WriteFile` exec it with the content on stdin, never through a shell. `WriteFile` is atomic: a temporary file, then a rename.

## Consequences

- The connector and the PC daemon link the same providers, and a WorkspaceClass's `spec.provider` names one.
- An implementation outside this repository is a Go package. A provider in another language needs a wire of its own, which this version does not define.
- The interface is no larger than the Machine API: exec and file calls are whole, and the caller reads status by `Get`, which an implementation answers from its backend's events. Streams and a caller's watch are additions.
- `Dial` on naru waits on naru-proxy, and until naru adds a route open to the caller alone, a Machine's port is guarded by the adapter's hourly token only.
- A workspace reaches the place's own services by egress, so on a naru host they run off the host, which drops connections from a Machine to its own addresses.
- The Machine API's aliases, shapes and GPUs stay unused until a workspace needs them.

## Signs the decision was wrong

- A provider needs an operation the Machine API does not have and cannot be built from it, so the interface was shaped by what naru offers rather than by what a bot needs.
- A second-language provider is wanted before a second Go one, which would show the boundary wanted a wire.
- The container provider cannot pass the suite on a Mac without a capability flag per case, which would show one interface covers backends that differ in kind.
- Callers poll `Get` often enough to load a backend, which would show events belong in the first version.
