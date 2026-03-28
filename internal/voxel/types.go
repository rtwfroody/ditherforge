// Package voxel provides shared utilities for voxel-based remeshing modes.
package voxel

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
