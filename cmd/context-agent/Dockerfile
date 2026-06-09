FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlutil/ urlutil/
COPY registry/ registry/
COPY cmd/context-agent/ cmd/context-agent/
WORKDIR /src/cmd/context-agent
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /context-agent .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /context-agent /context-agent
EXPOSE 8081
ENTRYPOINT ["/context-agent"]
