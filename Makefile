# OSRIVA Panel developer tasks (POSIX make). Windows users: see README for npm/go equivalents.
.PHONY: test test-pg vet fmt build api agent agents dashboard dev-api dev-dashboard docker clean

VERSION ?= 0.1.0-dev

test:            ## Go unit tests + dashboard typecheck
	go test ./...
	npm run typecheck

test-pg:         ## Include PostgreSQL integration tests (DESTROYS the target schema)
	@test -n "$$OSRIVA_TEST_DATABASE_URL" || (echo "set OSRIVA_TEST_DATABASE_URL to a disposable database" && exit 1)
	go test -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

build: api agent dashboard

api:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/osriva-api ./services/api/cmd/osriva-api

dashboard:
	npm run build:dashboard

dev-api:         ## API with settings from .env.dev.local
	node scripts/dev-api.mjs

dev-dashboard:
	npm run dev:dashboard

docker:
	docker compose build

clean:
	rm -rf bin apps/dashboard/.next

agent:           ## Agent for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/osriva-agent ./agent/cmd/osriva-agent

AGENT_TARGETS ?= linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 freebsd/amd64 freebsd/arm64 openbsd/amd64 netbsd/amd64

agents:          ## Agent for every target platform into dist/
	@for t in $(AGENT_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "osriva-agent $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
			-o dist/osriva-agent-$$os-$$arch$$ext ./agent/cmd/osriva-agent || exit 1; \
	done
