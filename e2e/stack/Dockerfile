# Tools image for the containerized end-to-end stack: the registry and
# identity-config stubs, the Valkey seeder, the router bootstrap, and the
# verifier.
#
# The build context is the repository root because e2e/stack replaces
# github.com/adcontextprotocol/adcp-go with ../.. — the router package it
# imports (to generate a config file that type-checks against the router's
# own struct) lives in that module.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY router/ router/
COPY e2e/stack/ e2e/stack/
WORKDIR /src/e2e/stack
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bootstrap ./cmd/bootstrap && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/registrystub ./cmd/registrystub && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/configstub ./cmd/configstub && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/verify ./cmd/verify

# Alpine rather than distroless: the stubs need a shell-less but wget-capable
# base so compose can health-check them, and the seeder and verifier are
# short-lived processes an operator will want to exec into when a run fails.
FROM alpine:3.20
RUN apk add --no-cache tini
COPY --from=build /out/bootstrap /usr/local/bin/bootstrap
COPY --from=build /out/registrystub /usr/local/bin/registrystub
COPY --from=build /out/configstub /usr/local/bin/configstub
COPY --from=build /out/seed /usr/local/bin/seed
COPY --from=build /out/verify /usr/local/bin/verify
ENTRYPOINT ["/sbin/tini", "--"]
