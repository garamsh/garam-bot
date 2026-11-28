# ADR 0096: Remove the gaon operator, its guest image and its chart

> Status: accepted
> Date: 2026-11-25

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0082](0082-import-garams-gaon-operator-and-keep-it-in-this-repository.md) imported garam's gaon operator. [ADR 0085](0085-ship-a-helm-chart-of-gaon-kinds-for-the-control-service-and-the-gaon-operator.md) shipped a Helm chart of gaon kinds for it and the control service. [ADR 0086](0086-restore-the-kubernetes-operator-and-keep-the-gaon-operator-unreleased.md) restored the Kubernetes operator. ADR 0086 kept the gaon operator unreleased, under the owner's decision that gaon's integration stays marked and not removed (garamsh/aurora#804, decision 1). It left whether the gaon operator is later deleted undecided.

gaon is now archived: `garamsh/gaon` was archived on 2026-11-21, and naru ADR 0114 stops it. Nothing will run the gaon operator, its guest image or its chart. The PM decided on garam-bot#1205, on 2026-11-25, to delete them rather than rename them in the move into garam-bot.

The guest supervisor, `cmd/gaon-guest`, is a gaon MicroVM's entrypoint and imports only `internal/gaon/guest`. The manager, the control service and the connector import nothing from `internal/gaon` or `tests/gaon`.

## Decision

1. **The gaon operator is removed.** That covers:
   - `cmd/gaon-operator`, `cmd/gaon-render` and `cmd/gaon-guest`;
   - `internal/gaon` and `tests/gaon`;
   - `tests/pairrun`, the pair run against garam with a stand-in for gaon's API;
   - `build/operator.Dockerfile` and `build/guest.Dockerfile`;
   - the chart `deploy/charts/garam-agent-operator-gaon`, and `hack/chartcheck`, which validated it against gaon's API types;
   - the Makefile targets `operator-image`, `guest-image`, `operator-image-check`, `guest-run`, `pair-run`, `pair-run-gaon`, `chart-check` and `helm`;
   - the requirement on `github.com/garamsh/gaon/lib/api`.
2. **This settles what ADR 0086 left open, and supersedes ADR 0082 and ADR 0085.** ADR 0086's restoration of the Kubernetes operator stands.
3. **The guest image's repository, `garam-bot/operator-guest` (garam-bot ADR 0023), is no longer published to.** Dropping it is garamsh/aurora's.

## Consequences

- `make ci` loses the chart check, and no chart remains in this module until the connector's chart (#445) adds one with a check of its own.
- The control service has no manifest or chart here. Its deployment is garamsh/aurora's, as it was for the gaon chart's values.
- `garam-bot/operator-guest` exists in the registry with nothing to publish to it.

## Signs the decision was wrong

- gaon, or a MicroVM platform shaped like it, comes back, and the operator for it is written again from nothing.

## Rejected alternatives

- **Renaming the gaon binaries to garam-bot names.** That keeps code for a platform that is archived, under a name that suggests it runs.
- **Keeping it unreleased as ADR 0086 did.** gaon is no longer discontinued but archived, so there is nothing left for the code to be kept for.
