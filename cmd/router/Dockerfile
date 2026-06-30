# syntax=docker/dockerfile:1.7
#
# Reproducible build for the TMP router.
#
# The image bytes are deterministic given the same source tree, the same
# Dockerfile, and a BuildKit-compatible builder (Docker 24+). Base images are
# pinned by digest; the Go build uses -trimpath / -buildvcs=false / -buildid=
# so the binary carries no path, VCS, or build-id entropy; SOURCE_DATE_EPOCH
# normalizes layer mtimes when set.
#
# See docs/tmp-router-reproducible-build.md for the verification procedure and
# how the resulting image digest relates to TEE attestation measurements.

# renovate: datasource=docker depName=library/golang
ARG GO_IMAGE=golang:1.26-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648
# renovate: datasource=docker depName=distroless/static-debian13
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

FROM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_DATE_EPOCH=0
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}
WORKDIR /src
COPY go.mod go.sum* ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlcanon/ urlcanon/
COPY router/ router/
COPY cmd/router/ cmd/router/
WORKDIR /src/cmd/router
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -buildid=" \
      -o /router .

FROM ${RUNTIME_IMAGE}
COPY --from=build /router /router
ENTRYPOINT ["/router"]
