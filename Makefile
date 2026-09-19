PLUGIN_NAME := header-credential-router
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ifeq ($(GOOS),darwin)
EXT := dylib
else ifeq ($(GOOS),windows)
EXT := dll
else
EXT := so
endif

OUT_DIR := dist/$(GOOS)/$(GOARCH)
OUT := $(OUT_DIR)/$(PLUGIN_NAME).$(EXT)

.PHONY: build test clean

build:
	mkdir -p $(OUT_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(OUT) .
	rm -f $(OUT_DIR)/$(PLUGIN_NAME).h

test:
	go test ./...

clean:
	rm -rf dist
