# Local deployment (testing only)

Run a garam-bot workspace in Docker: the `workspace` service serves the gRPC data plane (files and exec) as `garam-bot workspace` (ADR 0010, ADR 0040). **Local testing only, on a trusted host.** The listener is unauthenticated and stays on the container's loopback, so the operations are run inside the container.

## Contents
- Requirements
- Run
- Check
- Cleanup

## Requirements

One variable must be exported before `docker compose up`:

| Variable | Value | Why |
|---|---|---|
| `GARAM_BOT_DATA_DIR` | absolute host path for the workspace data | holds `workspace/`, bind-mounted into the workspace container at the same absolute path as on the host |

`workspace/` is written by the workspace process (root, holding no `DAC_OVERRIDE`) and by its Exec children (uid 10001). **Do not create it yourself.** Docker creates the missing bind source as `root:root`. On first use the workspace hands it to uid 10001 with mode `0775`, keeping its group, so the group write bit is how the workspace process still writes into it. A directory the host user created carries that user's group instead, the workspace process is then "other", and every write over the gRPC contract fails with `permission denied`. A `0777` does not help, because the handover sets `0775`. To reuse a `workspace/` an earlier run created, keep it as it is; to replace it, remove it as root. On Docker Desktop it must sit under a shared path (the project directory works). What the workload requires of storage is in `docs/architecture/deployment.md` §Volumes and ownership.

## Run

From this directory:

```sh
export GARAM_BOT_DATA_DIR="$PWD/data"
docker compose build
docker compose up -d
docker compose exec -T workspace /garam-bot workspace write hello.txt <<<'hello'
docker compose exec -T workspace /garam-bot workspace read hello.txt
docker compose exec -T workspace /garam-bot workspace exec cat /proc/self/limits
```

The operations dial the listener at `127.0.0.1:8081`, which is their default address, and print one JSON object (`read` prints the file's bytes). The workspace runs each `exec` in its own directory (`$GARAM_BOT_DATA_DIR/workspace`), dropped to uid 10001 in a fresh network namespace, with exactly the capabilities the executor needs (SETUID, SETGID, CHOWN, SYS_ADMIN, KILL — see `docker-compose.yml`, which records why KILL is among them) and under the executor's five resource caps. The exec UID is a dedicated one on purpose: a container shares the host's UID space, and the child's process cap counts every process of that UID, so an account the host also uses leaves the child unable to fork.

## Check

`./smoke.sh` runs the sequence above — `docker compose up` — and then asserts two things against the running deployment:

- **Both writers share one owner** (ADR 0008, ADR 0010). A write over the gRPC contract and a write by an exec'd child land under the exec account, each able to pick up what the other left.
- **Exec children carry all five resource caps.**

The `docker` job in `.github/workflows/ci.yml` runs the same script, so a change that stops this deployment from starting, or breaks the shared-owner contract, fails CI.

## Cleanup

```sh
docker compose down
```

The workspace data persists in `$GARAM_BOT_DATA_DIR/workspace`, which is owned by uid 10001 (the exec UID).
