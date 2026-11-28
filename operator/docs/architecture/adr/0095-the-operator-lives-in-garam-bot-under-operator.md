# ADR 0095: The operator lives in garam-bot under operator/

> Status: accepted; the push role of decision 3 amended by garam-bot ADR 0023
> Date: 2026-11-25

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The owner's rule is one repository per product (`garamsh/discussions` discussion 6, comment 19857166). garam-bot holds the runtime, the interfaces, the cluster connector and the PC daemon, each component with its own module and version tags. `garamsh/garam-bot#1205` moves garam-bot-operator into garam-bot under `operator/`. Its history is rewritten into that directory and joined to garam-bot's `dev` by a merge commit, so every file keeps its log and blame. The PM's decisions on that issue set what this record follows:

- `operator/` is its own Go module, `github.com/garamsh/garam-bot/operator`, with tags `operator/vX.Y.Z`.
- The cluster-connection interface moves to `api/cluster-connection/v1alpha1/`, beside garam-bot's other public interfaces, with its contents unchanged.
- The decision log moves to `operator/docs/architecture/adr/` and keeps its numbers.
- Workflows are joined with path filters per component, and image names, chart names and ECR repositories stay as they are.
- garam-bot's conventions govern `operator/`. `stack-kubebuilder.md` moves into them.

Four records name what the move changes. ADR 0051 and ADR 0081 publish a release from a git tag `v<version>` through `.github/workflows/release.yml`. ADR 0082 states the module path `github.com/garamsh/garam-agent-operator`. ADR 0092 places the interface at `interfaces/cluster-connection/v1alpha1/`, and records that this repository had not adopted `interfaces.md` into its conventions.

## Decision

1. **The operator is the module `github.com/garamsh/garam-bot/operator` at `operator/` in garam-bot.** Its packages keep their paths under that root. This amends ADR 0082's statement of the module path. The rest of ADR 0082 stands.
2. **A release is the git tag `operator/v<version>`, published by the repository's `.github/workflows/operator-release.yml`.** The version forms stay ADR 0081's, and the image tag is the version, with `operator/v` stripped. `hack/releaseversion` reads the prefix, and `hack/imageref` accepts a release version only in that workflow's run for that tag. A bare `v<version>` is the tag of garam-bot's other components and is refused. This amends ADR 0051's trigger and workflow and ADR 0081's tag form. Their release steps, the version scheme and the rule that a release is a tag on `main` stand.
3. **The release still pushes as `github-actions-garam-agent-operator-push`.** Its trust gains the subject `repo:garamsh@307152666/garam-bot@1311139677:ref:refs/tags/operator/v*` (`garamsh/aurora#865`). The image repositories do not change.
4. **The cluster-connection interface is `api/cluster-connection/v1alpha1/` in garam-bot's `api` module, outside this module.** This module requires a published version of `api`, never a `replace` to the directory (garam-bot ADR 0014 decision 1). The make targets `conformance-server` and `conformance-client` stay here and run the suites from `../api`. garam-bot's `docs/convention/interfaces.md` binds the interface. This amends ADR 0092's location and its statement that the convention was not adopted. The rest of ADR 0092 stands.
5. **garam-bot-operator's tags and issue numbers stay where they were.** The tags `v0.1.0-dev.1` to `v0.1.0-dev.5` name commits of the archived repository, and the commits of the rewritten history have other hashes. A record that names a garam-bot-operator commit or issue reads it there. The next release is `operator/v0.1.0-dev.6`.

## Consequences

- The workflows `operator-checks.yml` and `operator-test-e2e.yml` run on a promotion pull request that changes `operator/`, `api/` or the workflow. garam-bot's `ci.yml` leaves out a change to `operator/` alone.
- The e2e workflow's pull role names the environment `promotion-e2e` of the repository it runs in, so its trust needs garam-bot's subject too. The PM has asked `garamsh/infra` for it.
- The api module requires go-jose and Go 1.27.4, which the interface's reference server and suites need. That is the first time `api/`'s requirements grow past the Kubernetes libraries, one of the signs garam-bot ADR 0014 names.
- `make verify-pins` reads the operator's workflows at the repository root.

## Signs the decision was wrong

- A change to `api/` that the operator does not take breaks the operator's checks often enough that the path filter wants narrowing.
- A release tag reaches the wrong component's workflow, which would show that one tag namespace per component does not keep them apart.

## Rejected alternatives

- **Keeping the bare `v<version>` tag for the operator.** garam-bot's runtime releases from `v*` tags, and two components on one tag namespace cannot release on their own.
- **A `replace` from this module to `../api`.** garam-bot ADR 0014 forbids it for the root module, and the operator is a consumer of `api` as the root module is.
- **Keeping the interface under `operator/`.** It is garam's boundary as much as the connector's, and garam pins the `api` module, not the operator.
