#!/usr/bin/env bash
# Stands up the deployment README.md documents and fails unless the
# workspace's two writers share one owner and its exec children carry
# every resource cap. Contributors invoke it directly, as does the docker
# job in .github/workflows/ci.yml.
set -euo pipefail

cd "$(dirname "$0")"

export GARAM_BOT_DATA_DIR="${GARAM_BOT_DATA_DIR:-$PWD/data}"

# Compose logs are the diagnosis when a step fails, so print them before
# tearing the deployment down.
teardown() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    docker compose logs --no-color || true
  fi
  docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  exit "$status"
}
trap teardown EXIT

# --wait holds until compose reports the service up, so the readiness
# wait is compose's rather than a retry loop here. The data directory is
# not created here: the daemon creates the missing bind source as
# root:root, and the workspace's handover keeps the directory's group while
# giving it to the exec uid, so that group has to be the workspace
# process's own (README.md).
echo "==> deployment"
docker compose build
docker compose up -d --wait

# The operations are the binary's own subcommands, run in the workspace
# container against its loopback listener, which is the address they
# default to.
operation() {
  docker compose exec -T workspace /garam-bot workspace "$@" || true
}

# ADR 0008's file side: a write over the gRPC contract and a write by an
# exec'd child land under one owner, so either can pick up what the other
# left. The check writes through `garam-bot workspace write`, reads the
# owner from the workspace container, and then has an exec'd child modify
# that file and the contract overwrite the child's.
echo "==> a gRPC write lands in the workspace under the exec account"
written=$(operation write contract-write.txt <<<'written over the contract')
if ! grep -q '"bytes_written"' <<<"$written"; then
  echo "the write operation could not write into the workspace: ${written:-no output}" >&2
  exit 1
fi

# Either read can come back empty — the exec fails, or the file is not
# there — and an empty value compared against a uid reports a wrong owner
# for a file that was never read. Each is checked before it is compared,
# so "could not read it" and "it is the wrong uid" stay separate failures.
exec_uid=$(docker compose exec -T workspace printenv GARAM_BOT_EXEC_UID | tr -d '\r\n' || true)
if [ -z "$exec_uid" ]; then
  echo "could not read GARAM_BOT_EXEC_UID from the workspace container" >&2
  exit 1
fi

# The owner comes from a directory read, not from stat on the path. Write
# chowns a temporary file to the exec account and renames it into place
# (internal/workspace/files/), and where the workspace directory is a
# bind mount shared through a filesystem that caches per-path attributes
# — Docker Desktop's is one — stat of the new name keeps returning the
# pre-chown owner for as long as it is asked, while reading the directory
# returns the real one. Both agree on a native bind mount, so this is the
# read that is correct in CI and on a developer machine alike.
owner=$(docker compose exec -T workspace \
  find "$GARAM_BOT_DATA_DIR/workspace" -maxdepth 1 -name contract-write.txt \
  -printf '%U' | tr -d '\r\n' || true)
if [ -z "$owner" ]; then
  echo "could not read the owner of contract-write.txt in the workspace" >&2
  docker compose exec -T workspace ls -ln "$GARAM_BOT_DATA_DIR/workspace" >&2 || true
  exit 1
fi
if [ "$owner" != "$exec_uid" ]; then
  echo "the contract's file is owned by uid $owner, not the exec account $exec_uid" >&2
  docker compose exec -T workspace ls -ln "$GARAM_BOT_DATA_DIR/workspace" >&2 || true
  exit 1
fi

appended=$(operation exec sh -c 'echo appended >> contract-write.txt && cat contract-write.txt')
if ! grep -q 'appended' <<<"$appended"; then
  echo "an exec'd child could not modify what the contract wrote: ${appended:-no output}" >&2
  exit 1
fi

overwritten=$(operation write contract-write.txt <<<'overwritten over the contract')
if ! grep -q '"bytes_written"' <<<"$overwritten"; then
  echo "the contract could not overwrite the exec child's file: ${overwritten:-no output}" >&2
  exit 1
fi
echo "both writers share the workspace: uid $exec_uid owns what either one writes"

# Exec children carry the executor's five caps (ADR 0008, ADR 0010).
# The check runs the deployment's own exec operation against the
# deployment's own Exec RPC, and reads the caps from the kernel's view
# of the child rather than from a log line.
echo "==> Exec children carry every resource cap"
limits=$(operation exec cat /proc/self/limits)
missing=0
while IFS='|' read -r label value; do
  if ! grep -qE "$label +$value +$value " <<<"$limits"; then
    echo "the Exec child does not carry $label = $value" >&2
    missing=1
  fi
done <<'CAPS'
Max cpu time|60
Max address space|2147483648
Max file size|104857600
Max open files|1024
Max processes|64
CAPS
if [ "$missing" -ne 0 ]; then
  echo "$limits" >&2
  exit 1
fi
echo "cpu 60s, address space 2GiB, file size 100MiB, open files 1024, processes 64"
