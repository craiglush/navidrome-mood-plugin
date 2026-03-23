PLUGIN_NAME = mood-playlists
WASM_FILE = plugin.wasm
NDP_FILE = $(PLUGIN_NAME).ndp

.PHONY: build package clean test install

build:
	@if command -v tinygo > /dev/null 2>&1; then \
		echo "Building with TinyGo..."; \
		tinygo build -opt=2 -scheduler=none -no-debug \
			-o $(WASM_FILE) -target wasip1 -buildmode=c-shared .; \
	else \
		echo "Building with Go..."; \
		GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o $(WASM_FILE) .; \
	fi

package: build
	zip $(NDP_FILE) $(WASM_FILE) manifest.json

clean:
	rm -f $(WASM_FILE) $(NDP_FILE)

test:
	go test -race ./...

install: package
	@if [ -z "$(PLUGINS_DIR)" ]; then \
		echo "Usage: make install PLUGINS_DIR=/path/to/navidrome/data/plugins"; \
		exit 1; \
	fi
	cp $(NDP_FILE) $(PLUGINS_DIR)/
	@echo "Installed $(NDP_FILE) to $(PLUGINS_DIR)/"

# Build using Docker (no local Go/TinyGo required)
docker-build:
	docker run --rm -v "$(CURDIR):/src" -w /src tinygo/tinygo:latest \
		tinygo build -opt=2 -scheduler=none -no-debug \
		-o $(WASM_FILE) -target wasip1 -buildmode=c-shared .
	zip $(NDP_FILE) $(WASM_FILE) manifest.json
