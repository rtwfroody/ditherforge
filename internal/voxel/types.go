// Package voxel provides shared utilities for voxel-based remeshing modes.
package voxel

import "github.com/rtwfroody/ditherforge/internal/palette"

// PaletteConfig specifies how to determine the color palette.
// Exactly one of Palette, Inventory, or AutoPaletteN should be set.
type PaletteConfig struct {
	Palette         [][3]uint8             // explicit colors (--palette or default)
	Inventory       []palette.InventoryEntry // --inventory-file entries
	InventoryN      int                    // --inventory N
	InventoryMethod string                 // "nearest" or "hull"
	AutoPaletteN    int                    // --auto-palette N (0 = disabled)
}

// Config holds parameters for voxel remeshing.
type Config struct {
	NozzleDiameter float32 // nozzle width in mm
	LayerHeight    float32 // Z extrusion per layer in mm
	NoMerge        bool    // skip coplanar triangle merging
}

// ActiveCell represents one voxel cell to generate.
type ActiveCell struct {
	Col, Row, Layer int
	Cx, Cy, Cz     float32
	Color           [3]uint8
}

// CellKey is a canonical grid cell identifier.
type CellKey struct{ Col, Row, Layer int }
