# bindmount — Windows-only project. Cross-compiles cleanly from WSL/Linux.
GO      ?= go
GOFLAGS  = GOOS=windows GOARCH=amd64
BIN      = bin

.PHONY: all build test vet fmt clean

all: build

build:
	$(GOFLAGS) $(GO) build -o $(BIN)/bindmount.exe ./cmd/bindmount
	$(GOFLAGS) $(GO) build -o $(BIN)/bindmount-gui.exe ./cmd/bindmount-gui

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
	rm -rf $(BIN)
