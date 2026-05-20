FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum* ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY router/ router/
COPY cmd/router/ cmd/router/
WORKDIR /src/cmd/router
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /router .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /router /router
ENTRYPOINT ["/router"]
