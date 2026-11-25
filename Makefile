VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin/atlantis
IMAGE   ?= techblog/atlantis
# web/node_modules holds a third-party Go package (flatted); skip it.
PKGS     = $(shell go list ./... | grep -v /web/node_modules/)

.PHONY: all web build build-fake test test-web lint e2e dev docker clean

all: web build

## web: build the UI into internal/web/dist (embedded by the Go build)
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch $@

## build: build the atlantis binary into bin/
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/atlantis

## build-fake: build the pretend Deco mesh used by dev and e2e
build-fake:
	go build -o bin/atlantis-fake ./cmd/atlantis-fake

## test: Go tests with the race detector, then the UI unit tests
test: test-web
	go test -race $(PKGS)

test-web: web/node_modules
	cd web && npm test

## lint: go vet, golangci-lint (when installed), then typecheck and eslint
lint: web/node_modules
	go vet $(PKGS)
	@if command -v golangci-lint >/dev/null; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi
	cd web && npm run typecheck && npm run lint

## e2e: Playwright smoke tests against atlantis serve and the pretend mesh
e2e: web build build-fake
	./scripts/e2e.sh

## dev: run the UI with hot reload against the pretend mesh
dev: web/node_modules
	./scripts/dev.sh

## docker: build the image for this machine's platform
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

clean:
	rm -rf bin dist
	find internal/web/dist -mindepth 1 ! -name placeholder.html -delete
