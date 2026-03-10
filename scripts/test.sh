#!/bin/bash

# Agentize Test Script
# This script runs comprehensive tests for the Agentize project

set -e  # Exit on any error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get the directory where the script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}  Agentize Test Suite${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go is not installed or not in PATH${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Go version:$(go version)${NC}"
echo ""

# Step 0: Generate templ (if templ CLI is installed and .templ files exist)
if command -v templ &> /dev/null && [ -n "$(find . -name '*.templ' -not -path './.git/*' 2>/dev/null | head -1)" ]; then
    echo -e "${YELLOW}[0/7] Generating templ files...${NC}"
    if templ generate; then
        echo -e "${GREEN}✓ Templ generated${NC}"
    else
        echo -e "${RED}✗ templ generate failed${NC}"
        exit 1
    fi
    echo ""
fi

# Step 1: Check dependencies
echo -e "${YELLOW}[1/7] Checking dependencies...${NC}"
if ! go mod verify &> /dev/null; then
    echo -e "${YELLOW}  → Running go mod tidy...${NC}"
    go mod tidy
fi
echo -e "${GREEN}✓ Dependencies OK${NC}"
echo ""

# Step 2: Format check
echo -e "${YELLOW}[2/7] Checking code format...${NC}"
UNFORMATTED=$(gofmt -l .)
if [ -z "$UNFORMATTED" ]; then
    echo -e "${GREEN}✓ Code is properly formatted${NC}"
else
    echo -e "${YELLOW}  → Auto-formatting code...${NC}"
    gofmt -w .
    echo -e "${GREEN}✓ Code formatted${NC}"
fi
echo ""

# Step 3: Vet check
echo -e "${YELLOW}[3/7] Running go vet...${NC}"
if go vet ./...; then
    echo -e "${GREEN}✓ go vet passed${NC}"
else
    echo -e "${RED}✗ go vet found issues${NC}"
    exit 1
fi
echo ""

# Step 4: Build all packages
echo -e "${YELLOW}[4/7] Building all packages...${NC}"
if go build ./...; then
    echo -e "${GREEN}✓ Build passed${NC}"
else
    echo -e "${RED}✗ Build failed${NC}"
    exit 1
fi
echo ""

# Step 5: Run tests
echo -e "${YELLOW}[5/7] Running tests...${NC}"
echo ""
if go test -v ./...; then
    echo ""
    echo -e "${GREEN}✓ All tests passed${NC}"
else
    echo ""
    echo -e "${RED}✗ Some tests failed${NC}"
    exit 1
fi
echo ""

# Step 6: Test coverage
echo -e "${YELLOW}[6/7] Generating test coverage...${NC}"
COVERAGE_FILE="coverage.out"
if go test -coverprofile="$COVERAGE_FILE" ./...; then
    COVERAGE=$(go tool cover -func="$COVERAGE_FILE" | grep total | awk '{print $3}')
    echo -e "${GREEN}✓ Test coverage: ${COVERAGE}${NC}"
    echo -e "${BLUE}  Coverage report saved to: ${COVERAGE_FILE}${NC}"
    echo -e "${BLUE}  View HTML report: go tool cover -html=${COVERAGE_FILE}${NC}"
else
    echo -e "${RED}✗ Failed to generate coverage${NC}"
    exit 1
fi
echo ""

# Summary
echo -e "${BLUE}========================================${NC}"
echo -e "${GREEN}  All checks passed! ✓${NC}"
echo -e "${BLUE}========================================${NC}"

