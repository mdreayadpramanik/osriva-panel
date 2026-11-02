# OSRIVA Panel Core API — static binary on a distroless, non-root base.
# Build from the repository root:  docker build -f infrastructure/docker/api.Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY migrations ./migrations
COPY pkg ./pkg
COPY services ./services
ARG VERSION=0.1.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/osriva-api ./services/api/cmd/osriva-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/osriva-api /usr/local/bin/osriva-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/osriva-api"]
CMD ["serve"]
