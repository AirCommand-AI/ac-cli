set shell := ["bash", "-cu"]

_default:
    @just --list

build:
    @mkdir -p bin
    go build -o bin/aircom ./cmd/aircom

test:
    go test ./...

fmt:
    gofmt -w cmd internal
