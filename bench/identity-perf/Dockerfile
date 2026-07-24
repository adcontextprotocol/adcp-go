FROM golang:1.25-alpine AS build
WORKDIR /src
# The seeder imports targeting/identityhash from the parent module (single
# source of truth for the user-token hash). bench/identity-perf/go.mod
# points at ../.. via replace, so the build context has to include
# everything the parent module compiles against.
COPY go.mod go.sum ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlcanon/ urlcanon/
COPY urlutil/ urlutil/
COPY verify/ verify/
COPY bench/identity-perf/ bench/identity-perf/
WORKDIR /src/bench/identity-perf
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/configserver ./cmd/configserver && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seeder ./cmd/seeder && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadgen ./cmd/loadgen && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/writer ./cmd/writer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/configserver /usr/local/bin/configserver
COPY --from=build /out/seeder /usr/local/bin/seeder
COPY --from=build /out/loadgen /usr/local/bin/loadgen
COPY --from=build /out/writer /usr/local/bin/writer
ENTRYPOINT ["/sbin/tini", "--"]
