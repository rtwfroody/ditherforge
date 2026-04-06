#!/bin/bash
set -e
wails build -tags draco
go build -tags draco -o ditherforge-cli ./cmd/ditherforge
echo "Built build/bin/ditherforge (GUI) and ditherforge-cli (CLI)"
