VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)"
TAILWIND := bin/tailwindcss
TAILWIND_VERSION := v4.3.3

.PHONY: build build-linux generate templ css test rss rss-linux dev tools clean

## build: compile for this machine into bin/musdash
build:
	$(GOBUILD) -o bin/musdash ./cmd/musdash

## build-linux: compile release binaries for Linux servers into dist/
build-linux:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/musdash-linux-amd64 ./cmd/musdash
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/musdash-linux-arm64 ./cmd/musdash

## generate: rebuild the templ components and the stylesheet (both committed)
generate: templ css

templ:
	go tool templ generate

css: $(TAILWIND)
	$(TAILWIND) -i internal/web/assets/input.css -o internal/web/static/app.css --minify

## test: run every test except the memory test
test:
	go test -short ./...

## rss: measure idle memory on this machine
rss:
	go test ./test -run TestIdleRSS -count=1 -v

## rss-linux: measure idle memory inside a Linux container (the real target)
rss-linux:
	docker run --rm -v "$(CURDIR)":/src -v musdash-gocache:/root/.cache/go-build -v musdash-gomod:/go/pkg/mod \
		-w /src golang:1.27-alpine go test ./test -run TestIdleRSS -count=1 -v

## dev: run the control plane against ./data with the component gallery on
dev:
	go run ./cmd/musdash server -dev -data ./data -listen 127.0.0.1:8000

## tools: download the Tailwind standalone CLI for this machine
tools: $(TAILWIND)

$(TAILWIND):
	@mkdir -p bin
	@os=$$(uname -s | tr A-Z a-z | sed 's/darwin/macos/'); \
	 arch=$$(uname -m | sed 's/x86_64/x64/; s/aarch64/arm64/'); \
	 echo "downloading tailwindcss $(TAILWIND_VERSION) for $$os-$$arch"; \
	 curl -sfL -o $(TAILWIND) https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$$os-$$arch
	@chmod +x $(TAILWIND)

clean:
	rm -rf bin/musdash dist
