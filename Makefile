VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO      ?= go

.PHONY: build test dist serve clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/space ./cmd/space

test:
	$(GO) vet ./...
	$(GO) test ./...

# Static binaries for copying to a server without Go.
dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/space-linux-amd64  ./cmd/space
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/space-linux-arm64  ./cmd/space
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/space-darwin-arm64 ./cmd/space

serve: build
	./bin/space serve -dir ./snapshots

clean:
	/bin/rm -rf bin dist
