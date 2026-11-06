BINARY ?= kq

.PHONY: build test vet fmt install
GOBIN ?= $(shell go env GOPATH)/bin
.PHONY: build test test-integration lint fmt vet cover install uninstall release

build:
	go build -o $(BINARY) ./cmd/kq

test:
	go test ./...

test-integration:
	go test ./test/integration

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	gofmt -w $$(find cmd internal test -name '*.go')

cover:
	go test -coverprofile=coverage.out ./...

install:
	go install ./cmd/kq

uninstall:
	rm -f "$(GOBIN)/kq"

# kubectl plugin binaries must be named kubectl-<name> and placed on PATH.
plugin:
	go build -o kubectl-kq ./cmd/kq

release:
	goreleaser release --clean
