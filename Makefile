VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PKG     := ./cmd/claude-master
BIN     := claude-master

.PHONY: build test vet dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

# 配布物（goreleaser を使わずローカルで作る場合）。asset 名は
# install.sh / selfupdate と一致させること。
dist:
	rm -rf dist && mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    go build -trimpath -ldflags '$(LDFLAGS)' \
	    -o dist/claude-master_$${os}_$${arch} $(PKG) || exit 1; \
	  echo "built dist/claude-master_$${os}_$${arch}"; \
	done
	cd dist && shasum -a 256 claude-master_* > checksums.txt && cat checksums.txt

clean:
	rm -rf dist $(BIN)
