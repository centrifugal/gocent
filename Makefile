# Development flow for gocent. CI runs these same targets, so what passes here
# passes there.
#
# Run `make` or `make help` for the list.

GO ?= go

# The analysers track their latest releases: a check that only exists in the
# newest one is exactly the one worth running. Pin one to reproduce an older
# run, e.g. `make lint GOLANGCI_LINT_VERSION=v2.13.2`.
GOLANGCI_LINT_VERSION ?= latest
GOVULNCHECK_VERSION   ?= latest

GOLANGCI_LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   := $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# How long `make fuzz` runs each target.
FUZZTIME ?= 30s

# Statement coverage below this fails `make cover`.
COVERAGE_MIN ?= 90

# The Centrifugo release `make sync-proto` takes api.proto and proxy.proto
# from: a tag, such as v6.10.1. There is no default, so that nothing is synced
# from an unreleased branch by accident.
CENTRIFUGO_REF ?=
# Or a branch, such as master, set instead of CENTRIFUGO_REF on purpose: for
# work which needs Centrifugo changes not released yet. Sync from a tag again
# before releasing gocent.
CENTRIFUGO_BRANCH ?=

# The Centrifugo `make test-integration` talks to. testdata/centrifugo.json
# configures one to match.
GOCENT_TEST_ADDR    ?= http://localhost:8100/api
GOCENT_TEST_API_KEY ?= api_key

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the package
	$(GO) build ./...

.PHONY: test
test: ## Run the test suite
	$(GO) test -count=1 ./...

.PHONY: test-race
test-race: ## Run the test suite under the race detector
	$(GO) test -count=1 -race ./...

.PHONY: test-integration
test-integration: ## Run against a real Centrifugo (see testdata/centrifugo.json)
	GOCENT_TEST_ADDR=$(GOCENT_TEST_ADDR) GOCENT_TEST_API_KEY=$(GOCENT_TEST_API_KEY) \
		$(GO) test -count=1 -race -tags integration -run Integration ./...

.PHONY: vet
vet: ## Run go vet, with and without the integration tag
	$(GO) vet ./...
	$(GO) vet -tags integration ./...

.PHONY: generate
generate: ## Regenerate api_gen.go and proxy/proxy_gen.go from their protos
	$(GO) generate ./...

.PHONY: sync-proto
sync-proto: ## Copy api.proto and proxy.proto from Centrifugo at CENTRIFUGO_REF (a tag) or CENTRIFUGO_BRANCH, and regenerate
	@if [ -n "$(CENTRIFUGO_REF)" ] && [ -n "$(CENTRIFUGO_BRANCH)" ]; then echo "set CENTRIFUGO_REF or CENTRIFUGO_BRANCH, not both"; exit 1; fi
	@if [ -z "$(CENTRIFUGO_REF)$(CENTRIFUGO_BRANCH)" ]; then echo "set CENTRIFUGO_REF to a Centrifugo tag, e.g. make sync-proto CENTRIFUGO_REF=v6.10.1 - or CENTRIFUGO_BRANCH=master for unreleased changes"; exit 1; fi
	@ref="$(CENTRIFUGO_REF)$(CENTRIFUGO_BRANCH)"; \
	base=https://raw.githubusercontent.com/centrifugal/centrifugo/$$ref/internal; \
	curl -fsSL $$base/apiproto/api.proto -o api.proto.new || { rm -f api.proto.new; echo "no api.proto at Centrifugo $$ref"; exit 1; }; \
	curl -fsSL $$base/proxyproto/proxy.proto -o proxy.proto.new || { rm -f api.proto.new proxy.proto.new; echo "no proxy.proto at Centrifugo $$ref"; exit 1; }; \
	grep -q '^service CentrifugoApi' api.proto.new || { rm -f api.proto.new proxy.proto.new; echo "the file at Centrifugo $$ref is not its api.proto"; exit 1; }; \
	grep -q '^service CentrifugoProxy' proxy.proto.new || { rm -f api.proto.new proxy.proto.new; echo "the file at Centrifugo $$ref is not its proxy.proto"; exit 1; }; \
	mv api.proto.new api.proto; mkdir -p proxy; mv proxy.proto.new proxy/proxy.proto
	$(GO) generate ./...
	@echo "api.proto and proxy/proxy.proto are Centrifugo $(CENTRIFUGO_REF)$(CENTRIFUGO_BRANCH)'s: review git diff, then make check"

.PHONY: generate-check
generate-check: ## Fail if api_gen.go or proxy/proxy_gen.go is not what its proto generates
	@for f in api_gen.go proxy/proxy_gen.go; do cp $$f $$f.orig; done
	@$(GO) generate ./... || { for f in api_gen.go proxy/proxy_gen.go; do mv $$f.orig $$f; done; exit 1; }
	@stale=; for f in api_gen.go proxy/proxy_gen.go; do \
		if ! cmp -s $$f $$f.orig; then stale="$$stale $$f"; mv $$f.orig $$f; else rm $$f.orig; fi; \
	done; \
	if [ -n "$$stale" ]; then echo "stale:$$stale - run make generate"; exit 1; fi
	@echo "api_gen.go and proxy/proxy_gen.go are current"

.PHONY: no-deps
no-deps: ## Assert the package has no dependencies, no cgo and no unsafe
	@if grep -qE '^[[:space:]]*require' go.mod; then \
		echo "go.mod has grown a dependency:"; cat go.mod; exit 1; fi
	@files=$$($(GO) list -f '{{.CgoFiles}}' ./...); \
	for f in $$files; do if [ "$$f" != "[]" ]; then echo "cgo in the package"; exit 1; fi; done
	@if $(GO) list -f '{{join .Imports "\n"}}' ./... | grep -qx unsafe; then \
		echo "the package now imports unsafe"; exit 1; fi
	@echo "no dependencies, no cgo, no unsafe"

.PHONY: cover
# api_gen.go is left out of the figure. Its methods are one line each, all
# written by the same generator, and TestEveryMethodReachesItsEndpoint checks
# every one of them reaches its endpoint and batch command.
cover: ## Measure coverage of the hand-written code and fail below COVERAGE_MIN
	$(GO) test -count=1 -covermode=atomic -coverprofile=coverage.all.out . ./proxy
	@grep -vE '/(api_gen|proxy_gen)\.go:' coverage.all.out > coverage.out && rm coverage.all.out
	@$(GO) tool cover -func=coverage.out | tail -1
	@pct=$$($(GO) tool cover -func=coverage.out | tail -1 | grep -oE '[0-9.]+%' | tr -d '%'); \
	awk -v p="$$pct" -v m="$(COVERAGE_MIN)" 'BEGIN { if (p < m) { print "coverage " p "% is below " m "%"; exit 1 } }'

.PHONY: fuzz
# The fuzz targets `make fuzz` runs, as package:target, each for FUZZTIME: go
# test fuzzes one target at a time.
FUZZ_TARGETS = .:FuzzReplies .:FuzzBroadcastWithRetry .:FuzzExponentialBackoff ./proxy:FuzzHandler

fuzz: ## Fuzz reply decoding, broadcast retries, backoff and proxy requests, each for FUZZTIME
	@set -e; for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; target=$${t#*:}; \
		echo "$$pkg $$target"; \
		$(GO) test -count=1 -run '^$$' -fuzz "^$$target\$$" -fuzztime $(FUZZTIME) $$pkg; \
	done

# How many rounds `make stress` runs; `go test` runs 200.
STRESS_ROUNDS ?= 5000

.PHONY: stress
stress: ## Run the AutoBatch stress test for STRESS_ROUNDS rounds, with the race detector
	GOCENT_STRESS_ROUNDS=$(STRESS_ROUNDS) $(GO) test -count=1 -race -run '^TestAutoBatchStress$$' -timeout 30m .

.PHONY: bench
bench: ## Run the benchmarks
	$(GO) test -count=1 -run '^$$' -bench . -benchmem .

.PHONY: fmt
fmt: ## Format the source
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if anything needs formatting
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: fmt-check ## Run golangci-lint, with and without the integration tag
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags=integration ./...

.PHONY: sec
sec: ## Run govulncheck (gosec runs as part of make lint)
	$(GOVULNCHECK) ./...

.PHONY: clean
clean: ## Remove build and coverage artefacts
	rm -f coverage.out coverage.all.out api_gen.go.orig api.proto.new

.PHONY: check
check: fmt-check vet no-deps generate-check test-race cover lint sec ## Everything CI runs, except integration and fuzzing
	@echo
	@echo "check passed. With a Centrifugo running (see testdata/centrifugo.json) also run:"
	@echo "  make test-integration"
	@echo "  make fuzz            (FUZZTIME=2m for something thorough)"
