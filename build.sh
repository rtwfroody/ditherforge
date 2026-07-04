#!/bin/bash
set -e
wails build -tags "draco webkit2_41 tbbcontrol"
go build -tags "draco tbbcontrol" -o ditherforge-cli ./cmd/ditherforge
echo "Built build/bin/ditherforge (GUI) and ditherforge-cli (CLI)"
