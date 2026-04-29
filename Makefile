PREFIX ?= $(HOME)/.local
BIN    := $(PREFIX)/bin/pier
PKG    := ./cmd/pier

.PHONY: all build install uninstall test vet fmt clean

all: build

build:
	go build -o pier $(PKG)

install:
	@mkdir -p $(dir $(BIN))
	go build -o $(BIN) $(PKG)
	@echo "installed: $(BIN)"

uninstall:
	rm -f $(BIN)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -f pier
