# ADR 0023: Every garam-bot image publishes to `garam-bot/<image>` through one push role, e2e pulls through one pull role, and the operator runs in `garam-bot-system`

> Status: accepted
> Date: 2026-11-25

## Context

- garam-bot-operator moved into this repository under `operator/` (#1205, operator ADR 0095), so one repository now publishes the runtime's images and the operator's. They published under three names: `garam-bot/runtime`, `sherlock/agent` and `sherlock/workspace` (ADR 0009), and `garam-agent-operator/manager`, `garam-agent-operator/control` and the guest image (operator ADR 0078, ADR 0084). Each pushed as a role named for its old repository: `github-actions-sherlock` and `github-actions-garam-agent-operator-push`. The operator's e2e pulled as `github-actions-garam-agent-operator-pull`.
- garamsh/aurora names an image repository `<publishing repository>/<image>` (aurora ADR 0015), and a credential for the consumer that holds it (aurora ADR 0029~DEL). The publishing repository is now garam-bot for every image.
- The PM decided the names with cupa and garamsh/aurora on #1205 (PM decision, 2026-11-25). garamsh/aurora#866 created the repositories and the two roles.

## Decision

1. **Every image this repository publishes goes to `garam-bot/<image>`:** `garam-bot/runtime`, `garam-bot/agent`, `garam-bot/workspace` and `garam-bot/tools` for the root module, and `garam-bot/operator-manager` and `garam-bot/operator-control` for the operator. `garam-bot/operator-guest` was named for the gaon guest image, which operator ADR 0096 removes. ADR 0009's rules for a repository, one per image, immutable, a tag naming one commit, hold for each.
2. **Both release workflows push as `arn:aws:iam::486152169996:role/github-actions-garam-bot-push`,** which trusts `repo:garamsh@307152666/garam-bot@1311139677:ref:refs/tags/v*` and `.../ref:refs/tags/operator/v*`. `release.yml` publishes on a `v*` tag, and `operator-release.yml` on an `operator/v*` tag (operator ADR 0095).
3. **The operator's e2e pulls garam's test image as `arn:aws:iam::486152169996:role/github-actions-garam-bot-pull`,** which trusts the environment `promotion-e2e` of this repository.
4. **The operator's kustomize deployment runs in the namespace `garam-bot-system`.** Its object names take the prefix `garam-bot-operator-`, and its labels the name `garam-bot-operator`, as garamsh/aurora's Argo applications name it. The Secret `garam-credential` keeps its name.

This amends ADR 0009's repository names and operator ADR 0078's, ADR 0084's and ADR 0095 decision 3's repository and role names.

## Consequences

- The images already published stay in `sherlock/*` and `garam-agent-operator/*`, which nothing here pushes to any more.
- No release has pushed to the new repositories yet. The first `v*` tag and the first `operator/v*` tag after this lands are what show that the role and the repositories admit a push.
- The old roles' subjects for this repository are garamsh/aurora's to drop once garam-bot-operator is archived.
- `garam-bot/tools` is named for completeness: no workflow publishes a tools image.

## Signs the decision was wrong

- One push role for two components' releases lets a mistake in one workflow write to the other's repositories. A grant split per component would then be worth the second role.
- A consumer outside this repository still pulls from `sherlock/*` or `garam-agent-operator/*` after both have released here, which would show the move of names reached the publisher and not its readers.
