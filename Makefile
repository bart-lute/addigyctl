SPEC_URL ?= https://api.addigy.com/api/v2/documentation/godoc_swagger.json
SPEC     ?= api/godoc_swagger.json
OPENAPI3 ?= api/openapi3.json
BIN      ?= bin/addigyctl
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.DEFAULT_GOAL := help
.PHONY: help setup spec convert generate build all test install clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## One-time: pin oapi-codegen as a Go tool and fetch dependencies
	go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go get github.com/alecthomas/kong@latest
	go mod tidy

spec: ## Re-download the Swagger 2.0 spec from Addigy
	mkdir -p $(dir $(SPEC))
	curl -fsSL $(SPEC_URL) -o $(SPEC)

convert: ## Swagger 2.0 -> OpenAPI 3 (oapi-codegen needs OpenAPI 3)
	go run ./tools/swagger2openapi -in $(SPEC) -out $(OPENAPI3)

generate: convert ## Regenerate the API client in internal/addigy/gen
	go tool oapi-codegen -config oapi-codegen.yaml $(OPENAPI3)

build: ## Build bin/addigyctl
	go mod tidy
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/addigyctl

all: generate build ## generate + build

test: ## Run tests
	go test ./...

install: ## Install addigyctl into $GOBIN / $GOPATH/bin
	go install -ldflags "-X main.version=$(VERSION)" ./cmd/addigyctl

clean: ## Remove build output
	rm -rf bin
