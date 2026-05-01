// Package cacheblob implements the cache wire format: gob-encoded
// values inside a zstd stream. Extracted as a separate package so the
// encode/decode pair can be reused outside the diskcache directly
// (e.g. by the pipeline layer when it wants to encode once and hand
// the resulting bytes off to a write goroutine).
package cacheblob

import (
	"bytes"
	"encoding/gob"

	"github.com/klauspost/compress/zstd"
)

// Encode gob-encodes val and zstd-compresses the result. Returns the
// final blob suitable for storage in either cache tier.
func Encode(val any) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	if err := gob.NewEncoder(zw).Encode(val); err != nil {
		zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode is the inverse of Encode: zstd-decompresses blob and
// gob-decodes the result into out (which must be a pointer).
func Decode(blob []byte, out any) error {
	zr, err := zstd.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	defer zr.Close()
	return gob.NewDecoder(zr).Decode(out)
}
