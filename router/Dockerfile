FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY tmproto/ tmproto/
COPY router/ router/
COPY cmd/ cmd/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /router ./cmd/router

FROM scratch
COPY --from=build /router /router
ENTRYPOINT ["/router"]
