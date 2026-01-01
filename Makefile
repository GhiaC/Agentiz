.PHONY: build run test test-full test-verbose clean deps

# Build the agentize server
build:
	go build -o bin/agentize cmd/agentize/main.go

# Run the server (requires environment variables)
run:
	go run cmd/agentize/main.go

# Run with HTTP enabled
run-server:
	AGENTIZE_HTTP_ENABLED=true \
	AGENTIZE_FEATURE_HTTP=true \
	AGENTIZE_KNOWLEDGE_PATH=./knowledge \
	go run cmd/agentize/main.go

# Run comprehensive test suite (format, vet, tests, coverage)
test-full:
	@bash scripts/test.sh

# Run tests (simple)
test:
	go test ./...

# Run tests with verbose output
test-verbose:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

# Install dependencies
deps:
	export GOPROXY=https://goproxy.cn && go mod tidy
	export GOPROXY=https://goproxy.cn && go get github.com/go-echarts/go-echarts/v2

# Generate templ files
generate-templ:
	export GOPROXY=https://goproxy.cn && templ generate

