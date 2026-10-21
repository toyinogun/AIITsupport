# One file, three images. Pick the binary with --build-arg CMD=agent or
# CMD=toolserver (target app, the default), or CMD=seed with --target seed,
# which also carries the seed tree the seed Jobs read (spec 0015).
# Go cross compiles natively for the target platform, so no QEMU emulation is needed.

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG CMD
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

RUN test -n "$CMD" || (echo "set --build-arg CMD=agent, CMD=toolserver, or CMD=seed" >&2 && exit 1)

COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY eval ./eval
COPY db ./db

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/$CMD

# The seed image: the seed tree sits in the working directory, so the
# SEED_*_FILE defaults resolve. The *.local.yaml overlays (real emails and
# phones) are excluded in .dockerignore and reach a cluster only as a Secret.
FROM gcr.io/distroless/static-debian12:nonroot AS seed
COPY --from=build /out/app /app
WORKDIR /srv/noord
COPY db ./db
COPY seed ./seed
USER nonroot:nonroot
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
