# tracker — developer tasks.
#
# `make check` IS the gate. CI runs exactly this target, `scripts/verify.sh tracker` in the
# umbrella runs exactly this target, and so do you. The tool pins below are therefore the
# only ones — restating them in ci.yml is how the PR gate drifts away from the gate a human
# runs, so ci.yml deliberately does not.
#
# The toolchain mirrors holdfast, the org's other Go repo, rather than the `golangci-lint`
# the roadmap sketched in §6: staticcheck + govulncheck + vet is a strictly stronger set
# than golangci-lint's default linters, and one Go gate across both repos beats two.
STATICCHECK_VERSION ?= 2025.1.1
GOVULNCHECK_VERSION ?= v1.1.4

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w

IMAGE ?= tracker:dev

.PHONY: build test check fmt vet staticcheck govulncheck tidy clean image compose-check smoke run-db

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o tracker ./cmd/tracker

# -race because the server is a concurrent program (pool + HTTP handlers + signal
# handling) and a data race there would be a wrong location, not a crash.
#
# These tests need a REACHABLE DOCKER DAEMON: the spatial correctness this project rests
# on cannot be tested against a mock, so internal/testsupport starts a real PostGIS with
# testcontainers. It FAILS — never skips — if it cannot. A green `make check` on a machine
# without Docker would be a gate proving nothing.
test:
	go test -race -covermode=atomic ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needs:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

govulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# THE gate. CI runs exactly this.
check: fmt vet build test staticcheck govulncheck

# --- deployment ---------------------------------------------------------------

image:
	docker build -t $(IMAGE) .

compose-check:
	docker compose config -q && echo "docker-compose.yml is valid"

# Brings the real stack up and asserts /healthz answers 200 — the S0 acceptance. This is
# the packaging gate: "it compiled" does not prove the binary can find its database.
smoke:
	./scripts/smoke-compose.sh

tidy:
	go mod tidy

clean:
	rm -f tracker
