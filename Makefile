# bindmount — Windows-only project. Cross-compiles cleanly from WSL/Linux.
GO      ?= go
GOFLAGS  = GOOS=windows GOARCH=amd64
DIST     = dist
RELEASE  = release

.PHONY: all build test vet fmt clean release

all: build

build:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	$(GOFLAGS) $(GO) build -o $(DIST)/bindmount.exe ./cmd/bindmount
	cp scripts/bindmount-gui.ps1 $(DIST)/bindmount-gui.ps1

# Compile the Windows test binaries without running them (we're cross-compiling);
# run `go test ./...` on Windows itself for execution.
test:
	$(GOFLAGS) $(GO) test -exec=true ./...

vet:
	$(GOFLAGS) $(GO) vet ./...
	$(GO) vet ./...

fmt:
	gofmt -l .

clean:
	rm -rf $(DIST) $(RELEASE) bin

release: build
	rm -rf $(RELEASE)
	mkdir -p $(RELEASE)
	cd $(DIST) && 7z a -tzip ../$(RELEASE)/bindmount-windows-amd64.zip * >/dev/null
