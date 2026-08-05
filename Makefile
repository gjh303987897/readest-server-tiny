GOPROXY ?= https://goproxy.cn,direct

.PHONY: deps admin-deps admin-build test test-race vet security build run

deps:
	GOPROXY=$(GOPROXY) go mod download

admin-deps:
	npm --prefix web/admin ci --registry=https://registry.npmmirror.com

admin-build:
	npm --prefix web/admin run build

test:
	GOPROXY=$(GOPROXY) go test ./...

test-race:
	GOPROXY=$(GOPROXY) go test -race ./...

vet:
	GOPROXY=$(GOPROXY) go vet ./...

security:
	GOPROXY=$(GOPROXY) go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
	GOPROXY=$(GOPROXY) go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -quiet ./...

build: admin-build
	GOPROXY=$(GOPROXY) go build -trimpath -ldflags="-s -w" -o readest-server ./cmd/readest-server

run:
	GOPROXY=$(GOPROXY) go run ./cmd/readest-server
