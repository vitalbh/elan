.PHONY: build install test lint tidy herdr-build herdr-check herdr-install rex-build rex-install raycast-dev

# FTS5 is behind a build tag in mattn/go-sqlite3.
TAGS := sqlite_fts5
# sqlite-vec registers itself with sqlite3_auto_extension, which the macOS SDK
# header marks deprecated; the call goes to the bundled amalgamation, not to
# the system library, so the warning is noise.
export CGO_CFLAGS := -O2 -g -Wno-deprecated-declarations

build:
	go build -tags $(TAGS) -o bin/elan ./cmd/elan

# Stable path for the LaunchAgent and the MCP config (bin/ is rebuilt often).
install:
	go build -tags $(TAGS) -o $(HOME)/.local/bin/elan ./cmd/elan

test:
	go test -tags $(TAGS) -race ./...

lint:
	go vet -tags $(TAGS) ./...

tidy:
	go mod tidy

# herdr-app mods: copies lib.js into each mod and installs them.
HERDR_MODS := $(HOME)/.config/herdr-app/mods
herdr-build:
	rm -rf herdr/build && mkdir -p herdr/build
	for m in elan elan-window; do cp -R herdr/$$m herdr/build/$$m && cp herdr/lib.js herdr/build/$$m/lib.js; done

herdr-check: herdr-build
	for m in elan elan-window; do herdr-app check herdr/build/$$m || exit 1; done

herdr-install: herdr-check
	for m in elan elan-window; do mkdir -p $(HERDR_MODS)/$$m && cp herdr/build/$$m/* $(HERDR_MODS)/$$m/; done

# Rex frontend (rex/): elan-rex (Go, its own module) + elan.lua.
REX_CONFIG := $(HOME)/.config/rex
rex-build:
	cd rex && go vet ./... && go test ./... && go build -o ../bin/elan-rex .

rex-install: rex-build
	install -m 0755 bin/elan-rex $(HOME)/.local/bin/elan-rex
	cp rex/elan.lua $(REX_CONFIG)/elan.lua
	@grep -q 'require("elan")' $(REX_CONFIG)/init.lua 2>/dev/null || echo 'Adicione require("elan") ao $(REX_CONFIG)/init.lua'
