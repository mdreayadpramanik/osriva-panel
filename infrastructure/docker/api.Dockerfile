# OSRIVA Panel Core API — static binary on a distroless, non-root base.
# The image also carries agent builds for every supported platform, served at
# /api/v1/agent/download/<os>/<arch> so servers can enrol without external downloads.
# Build from the repository root:  docker build -f infrastructure/docker/api.Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY migrations ./migrations
COPY pkg ./pkg
COPY services ./services
COPY agent ./agent
ARG VERSION=1.0.0-dev
ARG AGENT_TARGETS="linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 freebsd/amd64 freebsd/arm64 openbsd/amd64 netbsd/amd64"
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/osriva-api ./services/api/cmd/osriva-api
RUN set -e; mkdir -p /out/agents; for t in ${AGENT_TARGETS}; do \
      os="${t%/*}"; arch="${t#*/}"; ext=""; [ "$os" = windows ] && ext=".exe"; \
      GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o "/out/agents/osriva-agent-$os-$arch$ext" ./agent/cmd/osriva-agent; \
    done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/osriva-api /usr/local/bin/osriva-api
COPY --from=build /out/agents /usr/share/osriva/agents
ENV OSRIVA_AGENT_DIST_DIR=/usr/share/osriva/agents
USER nonroot:nonroot
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/osriva-api"]
CMD ["serve"]
