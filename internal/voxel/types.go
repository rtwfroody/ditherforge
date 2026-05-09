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
//
// HalfIdx identifies which Split half produced the cell when the
// model has been split into two halves. 0 in the unsplit path; 0 or
// 1 in the split path. Downstream stages (Merge, export3mf) use this
// to partition cells per half so the 3MF output emits one
// `<object>` entry per half.
//
// Area is the total clipped surface area (in mesh units²) of all
// triangles that pass through this voxel cell. Used to weight the
// cell's vote in palette selection and to scale its error mass in
// dithering, so sliver voxels don't overwhelm full-coverage voxels.
// Zero means "unknown" — palette and dither code treat zero as unit
// weight so test/synthetic ActiveCell constructions need not set it.
type ActiveCell struct {
	Grid            uint8
	Col, Row, Layer int
	Cx, Cy, Cz      float32
	Color           [3]uint8
	HalfIdx         uint8
	Area            float32
}

// CellKey is a canonical grid cell identifier.
type CellKey struct {
	Grid            uint8
	Col, Row, Layer int
}
