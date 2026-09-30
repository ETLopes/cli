BINARY := cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ETLopes/cli/internal/cli.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the binary into ./bin
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

.PHONY: install
install: ## Install the binary to $GOPATH/bin
	go install -ldflags "$(LDFLAGS)" .

.PHONY: test
test: ## Run the test suite with the race detector
	go test -race -cover ./...

.PHONY: cover
cover: ## Write and open an HTML coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: build-nocgo
build-nocgo: ## Build without cgo (no live karaoke audio), as Linux and Windows ship
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-nocgo .

.PHONY: lint
lint: ## Vet, check formatting, and compile the builds without live audio
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi
	@# The audio stub only compiles when cgo is off or the OS is not macOS,
	@# which a Mac never builds by default; build those variants so it cannot rot.
	CGO_ENABLED=0 go build ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
	@echo "lint clean"

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: snapshot
snapshot: ## Build release archives locally without publishing
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
