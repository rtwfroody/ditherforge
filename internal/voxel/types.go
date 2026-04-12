// Package voxel provides shared utilities for voxel-based remeshing modes.
package voxel

import (
	"github.com/rtwfroody/ditherforge/internal/palette"
)

// PaletteConfig specifies how to determine the color palette.
// NumColors sets the total palette size. Locked colors are always included;
// remaining slots are filled from Inventory.
type PaletteConfig struct {
	NumColors int                      // total number of palette colors
	Locked    []palette.InventoryEntry // user-locked colors (always in palette); labels may be empty
	Inventory []palette.InventoryEntry // inventory entries for remaining slots
}

// Config holds parameters for voxel remeshing.
type Config struct {
	NozzleDiameter float32 // nozzle width in mm
	LayerHeight    float32 // Z extrusion per layer in mm
	NoMerge        bool    // skip coplanar triangle merging
}

// ActiveCell represents one voxel cell to generate.
type ActiveCell struct {
	Grid            uint8
	Col, Row, Layer int
	Cx, Cy, Cz     float32
	Color           [3]uint8
}

// CellKey is a canonical grid cell identifier.
type CellKey struct {
	Grid            uint8
	Col, Row, Layer int
}
