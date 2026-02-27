.PHONY: build test lint mock-binaries clean

BINARY = kodama
MOCK_CLAUDE = tests/mocks/claude/claude
MOCK_CODEX = tests/mocks/codex/codex

build:
	go build -o $(BINARY) ./cmd/kodama

test: mock-binaries
	go test ./...

test-verbose: mock-binaries
	go test -v ./...

test-coverage: mock-binaries
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint:
	@which golangci-lint > /dev/null 2>&1 || (echo "golangci-lint not installed, skipping lint" && exit 0)
	golangci-lint run ./...

mock-binaries:
	@mkdir -p tests/mocks/claude tests/mocks/codex
	go build -o $(MOCK_CLAUDE) ./tests/mocks/claude
	go build -o $(MOCK_CODEX) ./tests/mocks/codex

clean:
	rm -f $(BINARY) $(MOCK_CLAUDE) $(MOCK_CODEX) coverage.out coverage.html

run: build
	./$(BINARY)

run-tui: build
	./$(BINARY) tui
