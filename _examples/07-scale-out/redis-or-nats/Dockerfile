# Build from the repository root, so the replace directive in _examples/go.mod
# finds the neffos source:
#
#   docker build -f _examples/07-scale-out/redis-or-nats/Dockerfile -t neffos-scale-out .
#   docker run -p 8080:8080 -e REDIS_ADDR=host.docker.internal:6379 neffos-scale-out -addr :8080 server
FROM golang:1.26 AS build
WORKDIR /src

# Download modules first, so a code change does not repeat it.
COPY go.mod go.sum ./
COPY _examples/go.mod _examples/go.sum ./_examples/
RUN cd _examples && go mod download

COPY . .
RUN cd _examples && CGO_ENABLED=0 go build -o /out/app ./07-scale-out/redis-or-nats

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
CMD ["-addr", ":8080", "server"]
