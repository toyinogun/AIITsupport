# One file, two images. Pick the binary with --build-arg CMD=agent or CMD=toolserver.
# Go cross compiles natively for the target platform, so no QEMU emulation is needed.

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG CMD
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

RUN test -n "$CMD" || (echo "set --build-arg CMD=agent or CMD=toolserver" >&2 && exit 1)

COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY eval ./eval

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/$CMD

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
