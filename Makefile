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
generate: ## Regenerate api_gen.go from api.proto
	$(GO) generate ./...

.PHONY: generate-check
generate-check: ## Fail if api_gen.go is not what api.proto generates
	@cp api_gen.go api_gen.go.orig
	@$(GO) generate ./... || { mv api_gen.go.orig api_gen.go; exit 1; }
	@if ! cmp -s api_gen.go api_gen.go.orig; then \
		mv api_gen.go.orig api_gen.go; \
		echo "api_gen.go is stale: run make generate"; exit 1; fi
	@rm api_gen.go.orig
	@echo "api_gen.go is current"

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
	$(GO) test -count=1 -covermode=atomic -coverprofile=coverage.all.out .
	@grep -v '/api_gen.go:' coverage.all.out > coverage.out && rm coverage.all.out
	@$(GO) tool cover -func=coverage.out | tail -1
	@pct=$$($(GO) tool cover -func=coverage.out | tail -1 | grep -oE '[0-9.]+%' | tr -d '%'); \
	awk -v p="$$pct" -v m="$(COVERAGE_MIN)" 'BEGIN { if (p < m) { print "coverage " p "% is below " m "%"; exit 1 } }'

.PHONY: fuzz
fuzz: ## Fuzz reply decoding for FUZZTIME
	$(GO) test -count=1 -run '^$$' -fuzz FuzzReplies -fuzztime $(FUZZTIME) .

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
	rm -f coverage.out coverage.all.out api_gen.go.orig

.PHONY: check
check: fmt-check vet no-deps generate-check test-race cover lint sec ## Everything CI runs, except integration and fuzzing
	@echo
	@echo "check passed. With a Centrifugo running (see testdata/centrifugo.json) also run:"
	@echo "  make test-integration"
	@echo "  make fuzz            (FUZZTIME=2m for something thorough)"
