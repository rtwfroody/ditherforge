#!/bin/bash
set -e
go build -o ditherforge .
go build -o ditherforge-cli ./cmd/ditherforge
echo "Built ditherforge (GUI) and ditherforge-cli (CLI)"
