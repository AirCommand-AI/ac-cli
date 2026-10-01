set shell := ["bash", "-cu"]

_default:
    @just --list

build:
    @mkdir -p bin
    go build -o bin/aircom ./cmd/aircom

test:
    go test ./...

# Pass the pi binary as an argument, e.g. just test-pi /usr/local/bin/pi.
test-pi pi_path="pi":
    go test -tags pi_integration ./internal/pidriver -run '^TestRealPiRPC$' -args -pi-path '{{pi_path}}'

fmt:
    gofmt -w cmd internal
