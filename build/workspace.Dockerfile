# The workspace image (ADR 0006, ADR 0022): the garam-bot binary run as
# `garam-bot workspace`.
#
# The build context is the repository root — the build stage copies the
# module — and `make build-images` is the entry point that builds it.

FROM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src

# Layer caching: resolve dependencies before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# All dependencies are CGO-free (modernc.org/sqlite is pure Go), so the
# binary is fully static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/garam-bot ./cmd/garam-bot \
    && mkdir -p /out/data

# Workspace image: serves the gRPC contract (Read/Write/List/Exec) over
# loopback. The workspace hosts no tools and no plugin loader (ADR 0006).
#
# Minimal glibc base with a shell (Exec children are `sh -c`
# sub-processes) and the privilege drop made real: a privileged
# workspace chowns its data directory to the configured exec-uid at
# startup and drops Exec children to it (ADR 0005).
FROM debian:13-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad AS workspace
ARG REVISION=unknown
LABEL org.opencontainers.image.title="garam-bot-workspace" \
      org.opencontainers.image.source="https://github.com/garamsh/garam-bot" \
      org.opencontainers.image.revision="${REVISION}"

# The unprivileged UID exec children drop to (ADR 0005). useradd comes
# from the passwd package, which ships setuid helpers, so the hygiene
# strip runs after it (security.md: no setuid/setgid binaries at all —
# the privilege drop is the executor's job, not a suid helper's).
RUN useradd --system --uid 1001 --no-create-home agent \
    && find / -xdev -type f -perm /6000 -exec chmod a-s {} +

COPY --from=build /out/garam-bot /garam-bot
# Scratch space for the default workspace (data/workspace); mount the
# workspace volume here.
COPY --from=build /out/data /data

# Root with four capabilities (deploy/local/docker-compose.yml):
# SETUID/SETGID for the child credential drop, CHOWN for the workspace
# directory handover, SYS_ADMIN for the netns unshare (ADR 0006). On
# single-account runs the workspace runs as its own UID and the drop is
# a no-op.
USER root
ENTRYPOINT ["/garam-bot"]
CMD ["workspace"]
