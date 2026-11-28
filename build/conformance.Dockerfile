# The conformance image of interface 3 (api/workspace/v1alpha1/conformance.md):
# true, false and cat on the PATH, and an echo on TCP port 7. It exists for the
# Pod provider's suite and is never released, so `make build-images` does not
# build it; `make test-podworkspace` does, and loads it into kind.
# The build context is the repository root, admitted by .dockerignore.

FROM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/conformanceimage ./internal/podworkspace/conformanceimage \
    && mkdir -p /out/bin \
    && for command in true false cat; do ln /out/conformanceimage /out/bin/$command; done

FROM scratch
COPY --from=build /out/conformanceimage /conformanceimage
COPY --from=build /out/bin/ /bin/
ENV PATH=/bin
USER 65532:65532
ENTRYPOINT ["/conformanceimage"]
