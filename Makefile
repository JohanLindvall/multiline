.PHONY: all download check test bench fuzz update-tools fix

GOPATH := $(shell go env GOPATH)
GOBIN := $(GOPATH)/bin
PATH := $(GOBIN):$(PATH)

export PATH

all: fix check

download: go.mod go.sum
	go mod download

check: $(GOBIN)/golangci-lint test
	golangci-lint run ./...

test:
	go test -cover ./...

# Run every benchmark without the tests. The no-match benchmark is the one to
# watch: it must stay on the literal-prefilter fast path (~100ns/line), not
# fall back to running every start pattern regex.
bench:
	go test -run='^$$' -bench=. -benchmem ./...

# Run the conservation fuzzer (no line lost, duplicated, or reordered) for a
# short bounded burst; CI-friendly. Leave -fuzztime off for a long local hunt.
fuzz:
	go test -fuzz=FuzzConservation -fuzztime=30s .

# Force-install the latest version of each developer tool. Unlike a file target,
# a phony recipe runs every time, so @latest is actually re-fetched.
update-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# Install golangci-lint on demand for `check` when it is not already present.
$(GOBIN)/golangci-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

fix:
	gofmt -w .
	go mod tidy
