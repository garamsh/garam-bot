# Packages

Governs the package partition: which layout of `stack-go.md` §Directory layout this project is under, what makes a boundary exist, which way imports cross it, and where a new package goes. What each package holds is `docs/architecture/README.md` §Modules, and the rules every unit follows — what it is named for, what it may reference, when a new one is earned — are `structure.md`'s; this file is how the line around each package was drawn. Rationale: ADR 0001, ADR 0023, ADR 0040.

## The layout this project is under

`stack-go.md` §Directory layout decides the layout by what the module is, not by its size: the **Service layout** governs a module that ships a binary, the **Library layout** one that ships none. This module ships `cmd/garam-bot/main.go`, so it is under the Service layout, and it leaves that layout only by losing its binaries. Its size settles nothing, and no line count is recorded here for that reason.

The tree is the Service layout's. Implementations sit in sub-packages of the package declaring their interface, as the layout's `internal/<domain>/<pkg>/` lists them. `internal/memory/sqlite/` is the one implementation that imports a driver (`modernc.org/sqlite`), and `stack-go.md` §Directory layout's "An implementation that imports a driver (`pgx`, a vendor SDK) sits outside the domain package's import graph" holds for it: `go list -deps ./internal/memory` lists no `modernc.org/sqlite`, while `go list -deps ./internal/memory/sqlite` lists it.

**`service.go`** is where the layout decides a rule's scope. A domain owes one once it has behaviour of its own to offer; holding types and persisting them is not that, and neither is translating a request or wiring a runtime. `internal/bot/service.go` is the one, holding `Service`. A domain package here that has no `service.go` is not, on that fact alone, incomplete: `internal/workspace/` and `internal/memory/` hold shapes, and `internal/config/` resolves settings.

## Current decisions

Three cuts produce every package under `cmd/`, `internal/` and `pkg/`, applied in that order.

- **The workspace cuts first.** `internal/workspace/` and its sub-packages are what a workspace runs, and the sub-tree imports no other package in this module. Its only importer elsewhere is `cmd/garam-bot/`, whose `garam-bot workspace` operation subcommands are callers of the wire in `internal/workspace/` and of no backend (ADR 0027); `garam-bot workspace mcp` sits there too and is not a caller of the wire: it runs the operation subcommands as child processes and links no workspace package (ADR 0028). `internal/bot/` and `internal/claudeadapter/` import nothing under `internal/workspace/`.
- **A domain cuts second.** A top-level `internal/<name>/` is one domain, the unit `structure.md` defines under "A domain is the unit". Size decides none of them: `internal/workspace/` is the wire alone, so a caller links it without linking a backend (#525).
- **An implementation cuts third, and downward.** A sub-package holds either an implementation of an interface the package above declares — `internal/memory/sqlite/` for `memory.ConversationMemory` — or a backend nothing outside the sub-tree uses, which is `internal/workspace/files/` and `internal/workspace/shell/` behind `internal/workspace/server/`.

The direction is the rule: **no package's shipped code imports its own sub-package.** A sub-package imports upward or sideways inside its own sub-tree (`internal/workspace/server/` composing `files/` and `shell/`), never the reverse. `go list -f '{{.ImportPath}} {{.Imports}}' ./...` is the check.

**Each dependency interface a domain needs has its own file, named for it**, as §Directory layout's `internal/<domain>/<name>.go` requires (#1045). An interface a package declares for other code to call is an offer, and stays where the offer is: `bot.Service` in `internal/bot/service.go`, and `shell.Executor`, which `internal/workspace/server/` calls and `shell.ProcessExecutor` implements, in `internal/workspace/shell/process.go`. `rg -n '^\s*(type\s+)?\w+\s+interface\s*\{' internal pkg --glob '!*_test.go' --glob '!*.pb.go'` lists every declaration, grouped and indented ones included.

**The composition site is `cmd/garam-bot/main.go`.** `stack-go.md` §Project envelope places the site in `cmd/<binary>/main.go`. `NewRootCommand` builds `bot.NewService` for `run`, `bot.Probe` for `probe` and the workspace server for `workspace`, and `cmd/claude-adapter/main.go` builds the adapter's handler. No test outside a `cmd/<binary>/` package has to reach the wiring, so the exception that moved a site into a package `main.go` calls does not apply.

**A package another module links goes under `pkg/`,** as `stack-go.md` §Project envelope says: "`pkg/` is for code other modules import". The Go toolchain lets no other module import `internal/`. `pkg/naruworkspace/` is the only package there: the operator module's connector links it, and goal ⑤'s `garam-bot daemon` will link it from this module (#1209). Its sub-package `pkg/naruworkspace/secret/` implements the `CredentialStore` it declares, as the third cut places an implementation, so the provider's tests link no Kubernetes client. `go list -deps ./pkg/naruworkspace` lists no `k8s.io` package. Nothing under `pkg/` imports `internal/`.

A capability is not a package. It reaches a bot as an MCP server outside this repository, so adding one changes no import here (ADR 0001, ADR 0040).

## Where a new package goes

Answer in order. The first yes places it.

1. Does a workspace run it? It goes under `internal/workspace/`.
2. Does it implement an interface an existing package declares? It goes in a sub-package of that package, as `stack-go.md` §Directory layout lists implementations under `internal/<domain>/<pkg>/`; one that imports a driver must, by that section's driver rule.
3. Must two separately built programs agree on it? It is a top-level `internal/<name>/` holding the shape and no behaviour, as `internal/workspace/` and `internal/memory/` do.
4. Does another module import it? It goes under `pkg/`.
5. Has it earned a domain, as `structure.md` states under "A new domain is earned"? It is a top-level `internal/<name>/`, and its implementations nest beneath it.
6. Otherwise it is a file in the package that uses it. One consumer and no interface is not a boundary.

## Open questions

None.
