# Copyright 2026 Geda
# SPDX-License-Identifier: Apache-2.0
#
# Test image for the P2 cross-subnet gate. Not the product image -- that is
# docker/, built in P3.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY core/go.mod core/go.sum ./
RUN go mod download
COPY core/ ./
RUN CGO_ENABLED=0 go build -o /out/gatepeer ./internal/gatepeer

FROM alpine:3.21
# iproute2 for the static route that makes the two subnets reachable at all.
RUN apk add --no-cache iproute2
COPY --from=build /out/gatepeer /usr/local/bin/gatepeer
ENTRYPOINT ["/bin/sh", "-c"]
