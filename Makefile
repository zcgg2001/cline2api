.PHONY: help build test test-go test-web check-web vet check

GO ?= go
NODE ?= node

help:
	@echo "make build      Build CLI into build/bin/cline-proxy"
	@echo "make test       Run Go and frontend tests"
	@echo "make check      Run tests, JS syntax checks, vet and build"

build:
	$(GO) build -mod=readonly -o build/bin/cline-proxy ./cmd/cline-proxy

test: test-go test-web

test-go:
	$(GO) test -mod=readonly -race -shuffle=on -timeout 120s ./...

test-web:
	$(NODE) --test cmd/cline-proxy/web/tests/*.test.cjs

check-web:
	@for file in cmd/cline-proxy/web/js/*.js; do $(NODE) --check "$$file" || exit 1; done

vet:
	$(GO) vet -mod=readonly ./...

check: test check-web vet build
