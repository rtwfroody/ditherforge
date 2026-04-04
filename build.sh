#!/bin/bash
set -e
wails build
go build -o ditherforge-cli ./cmd/ditherforge
echo "Built build/bin/ditherforge (GUI) and ditherforge-cli (CLI)"
