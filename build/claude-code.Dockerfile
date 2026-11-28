# The claude-code workspace image: the claude-code adapter of the agent interface (cmd/claude-adapter,
# api/agent/v1alpha1) beside the Claude Code CLI it runs. The adapter serves port 7700 and starts
# `claude -p` for each task. The build context is the repository root, admitted by .dockerignore,
# and `make build-images` is the entry point that builds it.

FROM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src

# Layer caching: resolve dependencies before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/claude-adapter ./cmd/claude-adapter

FROM node:24-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530
ARG REVISION=unknown
LABEL org.opencontainers.image.title="garam-bot-claude-code" \
      org.opencontainers.image.source="https://github.com/garamsh/garam-bot" \
      org.opencontainers.image.revision="${REVISION}"

# The CLI as Anthropic publishes it, at one version and unmodified: its own authentication and
# its updater stay as they are, and the adapter turns the updater off for each run it starts. A
# new version is a change to this line, made on purpose.
RUN npm install --global --no-fund --no-audit @anthropic-ai/claude-code@2.1.329 \
 && npm cache clean --force

COPY --from=build /out/claude-adapter /usr/local/bin/claude-adapter

# Numeric, so the kubelet can check runAsNonRoot against it. `make build-images` checks the built
# image's Config.User for this. The Claude Code CLI refuses to bypass permissions as root.
USER 65532:65532
EXPOSE 7700
ENTRYPOINT ["/usr/local/bin/claude-adapter"]
