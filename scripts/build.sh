#!/bin/sh
set -eu
npm ci
npm run build
mkdir -p dist/bin
go test -race ./...
go vet ./...
go build -trimpath -o dist/bin/server ./cmd/server
python3 scripts/check.py
python3 scripts/test_recovery.py
python3 scripts/package.py
