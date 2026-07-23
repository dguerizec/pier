PREFIX ?= $(HOME)/.local
BIN    := $(PREFIX)/bin/pier
PKG    := ./cmd/pier
VERSION_FILE := internal/cli/root.go

.PHONY: all build install uninstall test vet fmt clean major minor patch tag check-clean

all: build

build:
	go build -o pier $(PKG)

install:
	@mkdir -p $(dir $(BIN))
	go build -o $(BIN) $(PKG)
	@echo "installed: $(BIN)"

uninstall:
	@if [ -x "$(BIN)" ]; then "$(BIN)" uninstall || true; fi
	rm -f $(BIN)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -f pier

check-clean:
	@test -z "$$(git status --porcelain)" || { echo "error: git worktree must be clean" >&2; exit 1; }

major minor patch: check-clean
	@set -eu; \
	current=$$(git tag --list 'v*' --sort=-version:refname | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$$/ { print; exit }'); \
	[ -n "$$current" ] || { echo "error: no stable vMAJOR.MINOR.PATCH tag found" >&2; exit 1; }; \
	IFS=.; set -- $${current#v}; major=$$1; minor=$$2; patch=$$3; \
	case "$@" in \
		major) major=$$((major + 1)); minor=0; patch=0 ;; \
		minor) minor=$$((minor + 1)); patch=0 ;; \
		patch) patch=$$((patch + 1)) ;; \
	esac; \
	next="v$$major.$$minor.$$patch"; \
	tmp="$(VERSION_FILE).tmp"; \
	awk -v version="$$next" '/^const devBaseVersion = "/ { print "const devBaseVersion = \"" version "\""; next } /^[[:space:]]*version = "/ { print "\tversion = \"" version "-dev\""; next } { print }' "$(VERSION_FILE)" > "$$tmp"; \
	mv "$$tmp" "$(VERSION_FILE)"; \
	git add "$(VERSION_FILE)"; \
	git commit -m "chore(release): bump version to $$next" -m "Advance the development defaults from the latest stable tag so local builds identify the next release before its annotated tag is published."

tag: check-clean
	@set -eu; \
	tag=$$(sed -n 's/^const devBaseVersion = "\(v[0-9][0-9.]*\)"/\1/p' "$(VERSION_FILE)"); \
	version=$$(sed -n 's/^[[:space:]]*version = "\(.*\)"/\1/p' "$(VERSION_FILE)"); \
	[ -n "$$tag" ] || { echo "error: could not read release version from $(VERSION_FILE)" >&2; exit 1; }; \
	[ "$$version" = "$$tag-dev" ] || { echo "error: source version $$version does not match $$tag-dev" >&2; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/$$tag" >/dev/null; then echo "error: tag $$tag already exists locally" >&2; exit 1; fi; \
	branch=$$(git branch --show-current); \
	[ -n "$$branch" ] || { echo "error: cannot tag from a detached HEAD" >&2; exit 1; }; \
	git tag -a "$$tag" -m "Release $$tag"; \
	if ! git push --atomic origin "HEAD:refs/heads/$$branch" "refs/tags/$$tag"; then \
		git tag -d "$$tag"; \
		exit 1; \
	fi
