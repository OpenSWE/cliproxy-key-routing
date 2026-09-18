NAME    := cliproxy-key-routing
VERSION := $(shell sed -n 's/.*pluginVersion = "\(.*\)"/\1/p' main.go)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter darwin,$(GOOS)),dylib,so)
OUT     := dist/$(NAME)-v$(VERSION).$(EXT)

.PHONY: build test clean
build: ## build the plugin shared library for this platform
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildmode=c-shared -o $(OUT) .
	rm -f dist/$(NAME)-v$(VERSION).h
test:
	go test ./...
clean:
	rm -rf dist
