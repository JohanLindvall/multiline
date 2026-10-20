.DEFAULT_GOAL := help
.PHONY: all help download check test vet lint fmt-check race examples bench fuzz tools update-tools fix

GOLANGCI_LINT_VERSION ?= $(shell cat .golangci-version)
GOLANGCI_LINT := $(CURDIR)/.tools/golangci-lint/$(GOLANGCI_LINT_VERSION)/golangci-lint
FUZZTIME ?= 30s

help:
	@printf '%s\n' \
		'make test        Run tests with coverage' \
		'make check       Check formatting, tests, vet, and pinned lint' \
		'make fix         Format Go files and tidy the module' \
		'make race        Run tests with the race detector' \
		'make examples    Run all example programs' \
		'make bench       Run benchmarks with allocation counts' \
		'make fuzz        Run four fuzzers (FUZZTIME=30s each)' \
		'make tools       Install the pinned linter locally'

all: check

download: go.mod go.sum
	go mod download

check: fmt-check test vet lint

test:
	go test -cover ./...

vet:
	go vet ./...

lint: $(GOLANGCI_LINT)
	"$(GOLANGCI_LINT)" run ./...

fmt-check:
	@unformatted="$$(gofmt -l .)" || exit 1; \
	if [ -n "$$unformatted" ]; then \
		printf 'Run make fix to format:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

race:
	go test -race ./...

examples:
	go run ./examples/simple
	go run ./examples/custom
	go run ./examples/cri

# Run every benchmark without the tests. The no-match benchmark is the one to
# watch: it must stay on the literal-prefilter fast path (~100ns/line), not
# fall back to running every start pattern regex.
bench:
	go test -run='^$$' -bench=. -benchmem ./...

# Run the conservation fuzzers for short bounded bursts; CI-friendly. Drop
# -fuzztime for a long local hunt. One fuzzer per property:
#   Conservation         no line lost, duplicated or reordered, no caps
#   CappedConservation   Lines accounting and per-entry Truncated attribution
#                        under WithMaxLines/WithMaxBytes
#   MultiKeyConservation interleaved keys, AddAt, FlushBefore and the eviction
#                        caps, none of which may drop text
#   RejoinConservation   the CRI stage repackages content without changing it
fuzz:
	go test -fuzz='^FuzzConservation$$' -fuzztime=$(FUZZTIME) .
	go test -fuzz='^FuzzCappedConservation$$' -fuzztime=$(FUZZTIME) .
	go test -fuzz='^FuzzMultiKeyConservation$$' -fuzztime=$(FUZZTIME) .
	go test -fuzz='^FuzzRejoinConservation$$' -fuzztime=$(FUZZTIME) ./cri

tools: $(GOLANGCI_LINT)

# Reinstall the pinned version. Edit .golangci-version to change local/CI tooling.
update-tools:
	@mkdir -p "$(dir $(GOLANGCI_LINT))"
	GOBIN="$(dir $(GOLANGCI_LINT))" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Install golangci-lint on demand for `check` when it is not already present.
$(GOLANGCI_LINT):
	@mkdir -p "$(dir $@)"
	GOBIN="$(dir $@)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

fix:
	gofmt -w .
	go mod tidy
