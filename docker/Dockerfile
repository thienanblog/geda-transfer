# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# The product image: gedad for NAS boxes, amd64 and arm64.
#
#   docker buildx build --platform linux/amd64,linux/arm64 \
#       -f docker/Dockerfile -t geda/gedad:latest .
#
# The build stage is pinned to the *build* platform and cross-compiles, rather
# than being emulated under QEMU per target. core/ is CGO-free by rule
# (core/README.md), which is what makes that possible -- and what keeps the
# arm64 image a compile rather than a twenty-minute emulation.

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

WORKDIR /src

# Manifests first: dependencies change far less often than code, so this layer
# survives most rebuilds.
COPY core/go.mod core/go.sum ./core/
COPY cli/go.mod cli/go.sum ./cli/
RUN cd cli && go mod download

COPY core/ ./core/
COPY cli/ ./cli/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN cd cli && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/gedad .

FROM alpine:3.21

# tzdata so that {HH}{mm}{ss} in a naming template is the local time the user
# expects rather than UTC; ca-certificates for nothing yet, and for any future
# outbound call. Both are a few hundred kilobytes.
RUN apk add --no-cache ca-certificates tzdata

# A fixed uid, because the volumes below are usually bind mounts from a NAS
# share and the ownership has to be predictable enough to chown.
RUN addgroup -g 1000 geda && adduser -D -u 1000 -G geda geda \
    && mkdir -p /state /data && chown geda:geda /state /data

COPY --from=build /out/gedad /usr/local/bin/gedad

# /state holds the ledger and the TLS identity and MUST be persisted: losing
# it makes this receiver look like a different machine to every paired phone.
# /data is where the photos land.
ENV GEDA_STATE_DIR=/state \
    GEDA_DEST=/data \
    GEDA_LISTEN=:47891 \
    GEDA_DISCOVERY_PORT=47890
VOLUME ["/state", "/data"]

EXPOSE 47891/tcp 47890/udp

USER geda

# The control socket is inside the container, so this checks the daemon is
# actually answering rather than merely still running.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD gedad status >/dev/null || exit 1

ENTRYPOINT ["gedad"]
CMD ["run"]
