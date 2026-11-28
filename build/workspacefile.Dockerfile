# The workspace file helper's image (cmd/workspacefile): the Pod workspace
# provider runs it as an init container, which copies the helper into the Pod's
# emptyDir, so a workspace's own image needs no shell and no cat for the file
# calls of interface 3. The build context is the repository root, admitted by
# .dockerignore, and `make build-images` is the entry point that builds it.

FROM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src

# Layer caching: resolve dependencies before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/workspacefile ./cmd/workspacefile

# Nothing but the binary: it has no shell and no libc, and needs neither.
FROM scratch
ARG REVISION=unknown
LABEL org.opencontainers.image.title="garam-bot-workspacefile" \
      org.opencontainers.image.source="https://github.com/garamsh/garam-bot" \
      org.opencontainers.image.revision="${REVISION}"

COPY --from=build /out/workspacefile /workspacefile

# Numeric, so the kubelet can check runAsNonRoot against it. `make build-images`
# checks the built image's Config.User for this.
USER 65532:65532
ENTRYPOINT ["/workspacefile"]
