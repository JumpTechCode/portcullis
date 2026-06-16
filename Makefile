# Portcullis developer tasks.
#
# `make verify` mirrors the checks that gate every pull request in CI.

GO            ?= go
PKG           ?= ./...
COVER_PROFILE ?= coverage.out
COVER_MIN     ?= 80

.PHONY: all verify build vet fmt fmt-check test race cover lint vuln tidy clean

all: verify

## verify: run the full set of CI gates locally
verify: build vet fmt-check race cover lint vuln

## build: compile all packages
build:
	$(GO) build $(PKG)

## vet: run go vet
vet:
	$(GO) vet $(PKG)

## fmt: format the tree in place
fmt:
	gofmt -s -w .

## fmt-check: fail if any file is not gofmt-clean
fmt-check:
	@unformatted=$$(gofmt -s -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

## test: run unit tests
test:
	$(GO) test $(PKG)

## race: run unit tests with the race detector
race:
	$(GO) test -race $(PKG)

## cover: run tests and enforce the coverage threshold
cover:
	COVER_PROFILE=$(COVER_PROFILE) ./scripts/coverage.sh $(COVER_MIN)

## lint: run golangci-lint
lint:
	golangci-lint run

## vuln: scan for known vulnerabilities
vuln:
	govulncheck $(PKG)

## tidy: tidy and verify go.mod / go.sum
tidy:
	$(GO) mod tidy

## clean: remove build and coverage artifacts
clean:
	rm -f $(COVER_PROFILE)
	$(GO) clean
