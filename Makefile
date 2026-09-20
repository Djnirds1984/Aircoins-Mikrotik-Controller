BIN     := bin
PKG     := ./...
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)

ROOT    := github.com/djnirds1984/aircoins-mikrotik-controller/internal/version
LDFLAGS := -s -w -X $(ROOT).Version=$(VERSION) -X $(ROOT).Commit=$(COMMIT) -X $(ROOT).BuildDate=$(DATE)

# SQLite is accessed through a pure-Go driver, so every cross target builds with
# CGO disabled. That is what makes the SBC and mini PC targets possible from any
# build host.
GOFLAGS := -trimpath
CGO     := CGO_ENABLED=0

.PHONY: all build run test vet fmt lint clean
.PHONY: build-linux-amd64 build-linux-arm64 build-linux-armv7 release

all: build

build:
	$(CGO) go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins ./cmd/aircoins
	$(CGO) go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-probe ./cmd/aircoins-probe

run: build
	./$(BIN)/aircoins -fake-router -data-dir ./data-dev

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w ./internal ./cmd

lint: vet fmt

build-linux-amd64:
	@mkdir -p $(BIN)
	$(CGO) GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-linux-amd64 ./cmd/aircoins
	$(CGO) GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-probe-linux-amd64 ./cmd/aircoins-probe

build-linux-arm64:
	@mkdir -p $(BIN)
	$(CGO) GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-linux-arm64 ./cmd/aircoins
	$(CGO) GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-probe-linux-arm64 ./cmd/aircoins-probe

build-linux-armv7:
	@mkdir -p $(BIN)
	$(CGO) GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-linux-armv7 ./cmd/aircoins
	$(CGO) GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/aircoins-probe-linux-armv7 ./cmd/aircoins-probe

release: build-linux-amd64 build-linux-arm64 build-linux-armv7

clean:
	rm -rf $(BIN) ./data-dev

