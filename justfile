# System 3 Justfile
# Usage: just <command>

# List available commands
default:
    @just --list

# Build the Go application
build:
    @echo "Building application..."
    go build -ldflags "-X main.Version=$(cat .version)" -o s3 .

# Format Go code
fmt:
    @echo "Formatting code..."
    go fmt ./...

# Run all tests with verbose output
test:
    @echo "Running tests..."
    go test -v ./...

# Install the binary to Go bin path
install:
    @echo "Installing to Go bin path..."
    go build -ldflags "-X main.Version=$(cat .version)" -o "$(go env GOPATH)/bin/s3"

# Run linter
lint:
    @echo "Running linter..."
    golangci-lint run

# Run the application
dev:
    @echo "Running application..."
    go run .

# Clean build artifacts
clean:
    @echo "Cleaning build artifacts..."
    rm -rf bin/
    go clean

# Install dependencies
deps:
    @echo "Installing dependencies..."
    go mod download

# Build and run in one command
run-build: build
    @echo "Running built application..."
    ./bin/app

