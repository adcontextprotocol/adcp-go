FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY ../go.mod ../go.sum ../
RUN go mod download
COPY . .
COPY ../tmp ../tmp
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /router .

FROM scratch
COPY --from=build /router /router
ENTRYPOINT ["/router"]
