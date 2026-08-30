FROM golang:1.27-alpine AS build
WORKDIR /src
# The seeder and loadgen import the context-agent's storage packages
# (mediabuystore, pkgconfigstore, topicstore, signalstore,
# suppressionstore) from the parent module so key layouts stay in
# lockstep with what the agent reads. bench/context-perf/go.mod points
# at ../.. via replace, so the build context has to include everything
# the parent module compiles against.
COPY go.mod go.sum ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlcanon/ urlcanon/
COPY urlutil/ urlutil/
COPY verify/ verify/
COPY bench/context-perf/ bench/context-perf/
WORKDIR /src/bench/context-perf
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seeder ./cmd/seeder && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadgen ./cmd/loadgen && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tmpregistry ./cmd/tmpregistry

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/seeder /usr/local/bin/seeder
COPY --from=build /out/loadgen /usr/local/bin/loadgen
COPY --from=build /out/tmpregistry /usr/local/bin/tmpregistry
ENTRYPOINT ["/sbin/tini", "--"]
