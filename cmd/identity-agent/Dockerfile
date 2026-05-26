FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlutil/ urlutil/
COPY cmd/identity-agent/ cmd/identity-agent/
WORKDIR /src/cmd/identity-agent
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /identity-agent .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /identity-agent /identity-agent
EXPOSE 8080
ENTRYPOINT ["/identity-agent"]
