pkg := "./cmd/loom"
bin := "loom"

default:
    @just --list

install:
    go install {{pkg}}

build:
    go build -o {{bin}} {{pkg}}

clean:
    rm -f {{bin}}

test:
    go test ./...

test-race:
    go test -race ./...

fmt:
    go fmt ./...

vet:
    go vet ./...

tidy:
    go mod tidy

check workflow:
    go run {{pkg}} check {{workflow}}

run workflow:
    go run {{pkg}} run {{workflow}}

check-all:
    #!/usr/bin/env bash
    set -euo pipefail
    for f in workflows/*.yaml; do
        echo "=== $f ==="
        go run {{pkg}} check "$f"
    done
