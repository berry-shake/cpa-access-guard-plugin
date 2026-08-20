PLUGIN := access-guard
PKG := ./cmd/cpa-access-guard
DIST := dist
WEB := web
EMBED_INDEX := internal/plugin/web/dist/index.html

.PHONY: test web-build build-linux-amd64 build-linux-arm64 build-linux clean

test:
	go test ./...

# Build the single-file web UI and place it where the Go embed expects it.
web-build:
	cd $(WEB) && npm install && VITE_HOSTED=1 npm run build
	cp $(WEB)/dist/index.html $(EMBED_INDEX)

build-linux-amd64: web-build
	mkdir -p $(DIST)/linux/amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared -buildmode=c-shared -o $(DIST)/linux/amd64/$(PLUGIN).so $(PKG)

build-linux-arm64: web-build
	mkdir -p $(DIST)/linux/arm64
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared -buildmode=c-shared -o $(DIST)/linux/arm64/$(PLUGIN).so $(PKG)

build-linux: build-linux-amd64 build-linux-arm64

clean:
	rm -rf $(DIST)
