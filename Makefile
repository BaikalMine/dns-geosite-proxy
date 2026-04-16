# dns-geosite-proxy Makefile
#
# Targets:
#   build         Cross-compile for ARMv7 (MikroTik container)
#   build-local   Build for local arch (dev/test)
#   docker-build  Build Docker image linux/arm/v7 via buildx
#   docker-save   Save image as RouterOS-compatible .tar and .tar.gz
#   docker-load   Load saved image into local Docker
#   download-dlc  Download latest dlc.dat from v2fly releases
#   update-deps   go get -u ./... && tidy
#   check-deps    Show available updates without applying them
#   tidy          go mod tidy
#   test          go test ./...
#   lint          golangci-lint run
#   vuln-check    govulncheck ./...
#   clean         Remove build/ directory

BINARY_NAME  := dns-proxy
IMAGE_NAME   := dns-geosite-proxy
IMAGE_TAG    ?= latest
REGISTRY     ?=
SKOPEO_IMAGE ?= quay.io/skopeo/stable:latest

# RouterOS container target
DOCKER_PLATFORM ?= linux/arm/v7
TARGET_ARCH    := armv7

# Go cross-compilation settings (linux/arm/v7 = GOARCH=arm + GOARM=7)
GOOS         := linux
GOARCH       := arm
GOARM        := 7
CGO_ENABLED  := 0

# Version info - injected into binary via ldflags at build time
VERSION      := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS      := -ldflags="-s -w -extldflags=-static \
  -X main.version=$(VERSION) \
  -X main.commit=$(COMMIT) \
  -X main.buildDate=$(BUILD_DATE)"

# Paths
SRC_DIR      := ./src
BUILD_DIR    := ./build
DOCKER_DIR   := ./docker
DATA_DIR     := ./data
IMAGE_TARBALL     := $(BUILD_DIR)/$(IMAGE_NAME)-$(TARGET_ARCH).tar
IMAGE_TARBALL_GZ  := $(IMAGE_TARBALL).gz
BUILD_IMAGE_TAR   := $(BUILD_DIR)/$(IMAGE_NAME)-$(TARGET_ARCH)-buildx.tar
BINARY_PATH       := $(BUILD_DIR)/$(BINARY_NAME)-$(TARGET_ARCH)

# dlc.dat source
DLC_URL      := https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat

.PHONY: all build build-local docker-build docker-save docker-load \
        update-deps check-deps tidy test lint vuln-check download-dlc clean help

# Default target
all: build

# Go build

## Cross-compile for ARMv7 (MikroTik target)
build: tidy
	@mkdir -p $(BUILD_DIR)
	@echo ">>> Building $(GOOS)/$(GOARCH)/v$(GOARM) binary..."
	cd $(SRC_DIR) && \
	  CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM) \
	  go build $(LDFLAGS) -o ../$(BINARY_PATH) .
	@echo ">>> Done: $(BINARY_PATH)"
	@ls -lh $(BINARY_PATH)

## Build for local architecture (for development and unit tests)
build-local:
	@mkdir -p $(BUILD_DIR)
	@echo ">>> Building for local arch..."
	cd $(SRC_DIR) && go build $(LDFLAGS) -o ../$(BUILD_DIR)/$(BINARY_NAME)-local .
	@echo ">>> Done: $(BUILD_DIR)/$(BINARY_NAME)-local"

# Docker

## Build Docker image for linux/arm/v7 using buildx
## Requires: docker buildx create --use (once per machine)
docker-build:
	@echo ">>> Building Docker image for $(DOCKER_PLATFORM)..."
	docker buildx build \
		--platform $(DOCKER_PLATFORM) \
		--provenance=false \
		--sbom=false \
		--load \
		-t $(REGISTRY)$(IMAGE_NAME):$(IMAGE_TAG) \
		-f $(DOCKER_DIR)/Dockerfile \
		--build-arg BUILD_DATE="$$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
		--build-arg VERSION=$(IMAGE_TAG) \
		.
	@echo ">>> Image: $(REGISTRY)$(IMAGE_NAME):$(IMAGE_TAG)"
	@docker images $(REGISTRY)$(IMAGE_NAME):$(IMAGE_TAG)

## Save Docker image as RouterOS-compatible .tar and .tar.gz
## MikroTik: /container/add file=dns-geosite-proxy-armv7.tar
docker-save: docker-build
	@mkdir -p $(BUILD_DIR)
	@echo ">>> Saving Docker buildx archive to $(BUILD_IMAGE_TAR)..."
	docker save -o $(BUILD_IMAGE_TAR) $(REGISTRY)$(IMAGE_NAME):$(IMAGE_TAG)
	@echo ">>> Repacking archive for RouterOS import..."
	rm -f $(IMAGE_TARBALL) $(IMAGE_TARBALL_GZ)
	docker run --rm \
		-v $(abspath $(BUILD_DIR)):/work \
		$(SKOPEO_IMAGE) copy \
		docker-archive:/work/$(notdir $(BUILD_IMAGE_TAR)) \
		docker-archive:/work/$(notdir $(IMAGE_TARBALL)):$(REGISTRY)$(IMAGE_NAME):$(IMAGE_TAG)
	rm -f $(BUILD_IMAGE_TAR)
	@echo ">>> Compressing $(IMAGE_TARBALL_GZ)..."
	gzip -c $(IMAGE_TARBALL) > $(IMAGE_TARBALL_GZ)
	@echo ">>> Saved:"
	@ls -lh $(IMAGE_TARBALL)
	@ls -lh $(IMAGE_TARBALL_GZ)
	@echo ""
	@echo "Upload to MikroTik flash/USB, then:"
	@echo "  /container/add file=$(IMAGE_NAME)-$(TARGET_ARCH).tar interface=veth1 envlist=dns-proxy"

## Load saved image into local Docker (for testing)
docker-load:
	docker load < $(IMAGE_TARBALL)

# Data

## Download latest dlc.dat from v2fly/domain-list-community releases
download-dlc:
	@mkdir -p $(DATA_DIR)
	@echo ">>> Downloading dlc.dat..."
	curl -fsSL --progress-bar $(DLC_URL) -o $(DATA_DIR)/dlc.dat
	@echo ">>> Size: $$(du -sh $(DATA_DIR)/dlc.dat | cut -f1)"

# Dependencies

## Update all Go modules to latest versions, then tidy
## After running: review go.sum diff before committing
update-deps:
	@echo ">>> Updating Go dependencies..."
	cd $(SRC_DIR) && go get -u ./...
	cd $(SRC_DIR) && go mod tidy
	@echo ">>> Done. Review go.sum changes before committing."

## Show available dependency updates without applying them
## Packages with [vX.Y.Z] have a newer version available
check-deps:
	@echo ">>> Checking for dependency updates..."
	@cd $(SRC_DIR) && go list -m -u all 2>/dev/null | grep -v ' indirect' | grep '\[' \
		|| echo "All direct dependencies are up to date."

## Run go mod tidy (sync go.mod/go.sum with imports)
tidy:
	cd $(SRC_DIR) && go mod tidy

# Quality

## Run all unit tests with verbose output
test:
	cd $(SRC_DIR) && go test ./... -v -count=1 -timeout 30s

## Run linter
## Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
lint:
	cd $(SRC_DIR) && golangci-lint run ./...

## Check for known CVEs in dependencies
## Install: go install golang.org/x/vuln/cmd/govulncheck@latest
vuln-check:
	cd $(SRC_DIR) && govulncheck ./...

# Housekeeping

## Remove build artifacts
clean:
	@rm -rf $(BUILD_DIR)
	@echo ">>> Cleaned $(BUILD_DIR)"

## Show this help
help:
	@echo ""
	@echo "dns-geosite-proxy build targets"
	@echo "================================"
	@echo "  make build         Cross-compile for ARMv7 (MikroTik)"
	@echo "  make build-local   Build for local arch (dev/test)"
	@echo "  make docker-build  Build Docker image linux/arm/v7"
	@echo "  make docker-save   Save image as RouterOS-compatible .tar and .tar.gz"
	@echo "  make docker-load   Load saved image into local Docker"
	@echo "  make download-dlc  Download latest dlc.dat from v2fly"
	@echo "  make update-deps   go get -u ./... && go mod tidy"
	@echo "  make check-deps    Show available updates (no changes)"
	@echo "  make tidy          go mod tidy"
	@echo "  make test          Run unit tests"
	@echo "  make lint          Run golangci-lint"
	@echo "  make vuln-check    Check CVEs via govulncheck"
	@echo "  make clean         Remove build/ directory"
	@echo ""
	@echo "Variables (override on CLI):"
	@echo "  IMAGE_TAG=v1.2.3   Set image tag (default: latest)"
	@echo "  REGISTRY=ghcr.io/  Set registry prefix"
	@echo ""
