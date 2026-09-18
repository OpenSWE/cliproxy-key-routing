NAME    := cliproxy-key-routing
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter darwin,$(GOOS)),dylib,so)
OUT     := dist/$(NAME).$(EXT)

.PHONY: build test clean
build: ## build the plugin shared library for this platform
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildmode=c-shared -o $(OUT) .
	rm -f dist/$(NAME).h
test:
	go test ./...
clean:
	rm -rf dist
