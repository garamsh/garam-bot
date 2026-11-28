# Workspace provider v1alpha1 conformance

The tests of `README.md`'s requirements: what each one is, and how a new implementation runs them.

## Running the suite

- **The suite** is `conformance/`: every case in `Cases()`, each a subtest named `<requirement>/<case>`. An implementation plugs in as a `conformance.Subject` (its `Provider`, the conformance image as it runs it, the resources each workspace gets and the timeout of each wait) and runs `conformance.Run(t, subject)`. The suite drives the implementation through `Provider` alone.
- **`TestReference`** runs it against `workspacefake`, twice: offering every capability and offering none, so each case a capability gates runs on one side and skips on the other. `make test` runs it.
- **The conformance image** is what a real implementation runs the suite with. It carries `true`, `false` and `cat` on its `PATH`, and its entrypoint echoes every byte it reads on TCP port 7 (`conformance.EchoPort`). `workspacefake` behaves as that image and runs none.
- **A case a capability gates** skips when the implementation reports the other side, with that reason.
- **The caller suite** is `CallerCases()` in the same package, run with `conformance.RunCaller(t, caller)`: the case of VER-2, the one requirement that binds the caller. A caller plugs in as a `conformance.Caller` (read a workspace's phase and a snapshot's through a `Provider`), and the suite serves it a provider that answers a phase a later release may add. `TestReferenceCaller` runs it against `conformance.ReferenceCaller`, the smallest caller that keeps VER-2. The bot's driver (garam-bot#1281) binds its own code to the same cases.
- **A pending case** is skipped with its reason. It counts as named, not as passing.
- **Every workspace and snapshot a case makes** is deleted when the case ends, by a name no other case uses.

## Requirements and their tests

| Requirement | Tests |
|---|---|
| NAME-1 | `NAME-1/a name that is not a DNS label is refused`. The other-caller half needs two callers of one backend, which the suite does not hold, and is pending with LIFE-5's |
| SPEC-1 | `SPEC-1/a spec that breaks a rule is refused`: a tag, each zero resource, port 0, a repeated port, an env name holding `=`, a prefix not in canonical form. `TestSpecCheck_SPEC1` holds every rule of `Spec.Check` |
| LIFE-1 | `LIFE-1/create is idempotent by name and refuses another spec` |
| LIFE-2 | `LIFE-2/a created workspace runs, is listed, and is read without its env values` |
| LIFE-3 | `LIFE-3/stop and start are idempotent and reach their phase` |
| LIFE-4 | `LIFE-4/delete is idempotent and the name is freed` |
| LIFE-5 | `LIFE-5/a name the caller does not have is not found`. The other-caller half needs two callers of one backend, which the suite does not hold |
| DATA-1 | `DATA-1/the data volume survives a stop and a start` |
| RUN-1 | `RUN-1/guest calls to a stopped workspace are refused` |
| EXEC-1 | `EXEC-1/exec returns the exit code and the output whole`, its input written as a file |
| EXEC-2 | `EXEC-2/stdin reaches the command with the stdin capability`, `EXEC-2/stdin is refused without the stdin capability` |
| FILE-1 | `FILE-1/a written file reads back whole`, `FILE-1/a path that is not absolute and clean, or is a system directory, is refused`. `TestCheckPath_FILE1` |
| FILE-2 | `FILE-2/a read of a missing file is file not found` |
| ISO-1 | `ISO-1/one workspace does not see another's files` |
| DIAL-1 | `DIAL-1/dial reaches a spec port and refuses another`. That the listener authenticates every connection is interface 2's AUTH-1, tested there |
| NET-1 | `NET-1/egress rules are refused without the egress capability`, `NET-1/egress rules are taken with the egress capability`. `NET-1/a workspace sends nothing its egress rules do not allow`, pending a conformance image that reports what it can reach |
| SNAP-1 | `SNAP-1/snapshot is idempotent by name and refuses another workspace's` |
| SNAP-2 | `SNAP-2/a restore holds the snapshot's data and outlives its source`, `SNAP-2/a restore of another disk size is refused` |
| SNAP-3 | `SNAP-3/snapshot is refused without the snapshot capability` |
| VER-1 | `SpecRelease` in this package, read by review: the release is a constant, not behaviour |
| VER-2 | `VER-2/a caller reads a phase it does not know as unknown`, `VER-2/a caller reads a snapshot phase it does not know as unknown`, in the caller suite |
