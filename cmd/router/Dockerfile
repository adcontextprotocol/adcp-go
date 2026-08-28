FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY tmproto/ tmproto/
COPY targeting/ targeting/
COPY urlcanon/ urlcanon/
COPY registry/ registry/
COPY router/ router/
COPY cmd/router/ cmd/router/
WORKDIR /src/cmd/router
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /router .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /router /router
EXPOSE 8080
ENTRYPOINT ["/router"]
