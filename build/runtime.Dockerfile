# The runtime image: `garam-bot run` runs one Bot of interface 1 from a mounted
# file, and `garam-bot probe` is the container's readiness and liveness probe
# (ADR 0033). The build context is the repository root, admitted by
# .dockerignore, and `make build-images` is the entry point that builds it.

FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src

# Layer caching: resolve dependencies before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/garam-bot ./cmd/garam-bot \
    && mkdir -p /out/status

# distroless static: no shell and no libc, and the binary needs neither. It
# runs as nonroot (uid and gid 65532) and asks for no capability, so a
# namespace enforcing PodSecurity `restricted` admits it.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
ARG REVISION=unknown
LABEL org.opencontainers.image.title="garam-bot-runtime" \
      org.opencontainers.image.source="https://github.com/garamsh/garam-bot" \
      org.opencontainers.image.revision="${REVISION}"

COPY --from=build /out/garam-bot /garam-bot
# The status file's directory, writable by the runtime's user. A Pod with a
# read-only root filesystem mounts an emptyDir here instead.
COPY --from=build --chown=65532:65532 /out/status /run/garam-bot

# Numeric, so the kubelet can check runAsNonRoot against it: it cannot resolve
# a user name from the image and refuses the container. `make build-images`
# checks the built image's Config.User for this.
USER 65532:65532
ENTRYPOINT ["/garam-bot"]
CMD ["run"]
