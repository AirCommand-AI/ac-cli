set shell := ["bash", "-cu"]

_default:
    @just --list

build:
    @mkdir -p bin
    go build -o bin/ac-cli ./cmd/ac-cli

test:
    go test ./...

fmt:
    gofmt -w cmd internal
