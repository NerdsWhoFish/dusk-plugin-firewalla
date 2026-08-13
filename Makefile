BIN := dusk-plugin-firewalla
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build check test vet nocgo live clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

check: vet nocgo test

vet:
	go vet ./...

# Enforces the no cgo rule. A cgo dependency arrives transitively and unnoticed,
# and takes cross compilation and arm64 with it.
nocgo:
	CGO_ENABLED=0 go build ./...

test:
	go test -race ./...

# Against a real box, because nothing here can prove that its Redis holds the
# fields this reads or that its sshd permits forwarding. Skipped without a
# target. Read only: it opens a tunnel, reads, and closes.
live:
	FIREWALLA_HOST='$(FIREWALLA_HOST)' FIREWALLA_USER='$(FIREWALLA_USER)' \
	FIREWALLA_HOST_KEY='$(FIREWALLA_HOST_KEY)' FIREWALLA_PASSWORD='$(FIREWALLA_PASSWORD)' \
	FIREWALLA_PRIVATE_KEY='$(FIREWALLA_PRIVATE_KEY)' \
	FIREWALLA_KEY_PASSPHRASE='$(FIREWALLA_KEY_PASSPHRASE)' \
	go test -count=1 -run TestLive -v ./pkg/firewalla

clean:
	rm -rf bin
