# ADR 0003: Secret-free execution domain

> Status: accepted
> Date: 2026-07-25 (revised in place 2026-10-28; 2026-11-11; 2026-11-28)

## Context

A bot runs code it did not write (commands a model chooses, programs it fetches). Any secret value present in the execution domain — environment variables, files, mounted credentials — must be considered exposed to whatever the bot executes. Filters (command inspection, guard models) are probabilistic and bypassable by obfuscation; absence is not. A hard requirement exists in the other direction: programs such as the GitHub CLI need both the workspace (inside the domain) and a credential (outside it) to be useful.

## Decision

Secret values never materialize in the code-execution domain. Exposure is prevented by absence, not by filtering.

- **The execution domain is the workspace and the Exec children it runs.** Those start from a minimal scrubbed environment and inherit nothing of the process that serves them (ADR 0008). The workspace process itself holds no credential either: it reads none, and the image that runs it ships none.
- **A secret reaches a party that needs it through an explicit channel outside the domain**: a file or environment the party's own deployment mounts for it (the bot's garam token reaches the runtime this way, `runtime.md`), never through code the workspace executes.
- **High-sensitivity operations stay outside the domain**: a trusted party performs the whole operation and returns a summary.
- **Secrets must not be placed into a model's conversation context** — container design cannot fix a secret a model has already seen.
- Capability misuse (using a credential in an unintended way) is bounded by the credential's own scope (fine-grained, minimal permissions), not by the workspace.

## Consequences

- Secret theft from the execution domain is structurally impossible for credentials the deployment manages, including prompt-injection-driven exfiltration of values.
- Threat model split: guard models address *accidents* (defense-in-depth); structural absence addresses *adversarial* exfiltration of values.
- User-supplied secrets pasted into chat are out of scope; the data-flow rule can only govern what the deployment injects.
