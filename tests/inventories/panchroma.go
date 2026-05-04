// Package inventories exposes filament inventories as Go literals for
// use by tests and bench tools. The same data lives in text form at
// internal/collection/builtins/*.txt for the production pipeline; the
// Go literal copy here keeps tests hermetic (no disk paths) at the
// cost of one extra place to update when colors are added.
//
// If a hex value here drifts from the .txt file the only consequence
// is a benchmark / regression number that doesn't match what users
// would actually get; the production pipeline reads the .txt
// authoritatively. Worth syncing periodically anyway.
package inventories

import "github.com/rtwfroody/ditherforge/internal/palette"

// Panchroma returns the 28-color Polymaker Panchroma Basic filament
// set, the same inventory referenced by tests/color_test.go fixtures.
// Mirrors internal/collection/builtins/panchroma_basic.txt.
func Panchroma() []palette.InventoryEntry {
	return []palette.InventoryEntry{
		{Color: [3]uint8{0x08, 0x0A, 0x0D}, Label: "Black"},
		{Color: [3]uint8{0x55, 0x33, 0x1A}, Label: "Brown"},
		{Color: [3]uint8{0xE7, 0x2F, 0x1D}, Label: "Red"},
		{Color: [3]uint8{0xD6, 0x02, 0x12}, Label: "Wine Red"},
		{Color: [3]uint8{0xF2, 0x45, 0x74}, Label: "Magenta"},
		{Color: [3]uint8{0xF1, 0xA1, 0xAF}, Label: "Pink"},
		{Color: [3]uint8{0xF6, 0x74, 0x05}, Label: "Orange"},
		{Color: [3]uint8{0xFF, 0xE8, 0x00}, Label: "Yellow"},
		{Color: [3]uint8{0xEE, 0xD2, 0x30}, Label: "Lemon Yellow"},
		{Color: [3]uint8{0xEE, 0xD1, 0xA8}, Label: "Cream"},
		{Color: [3]uint8{0xC2, 0xAB, 0x72}, Label: "Beige"},
		{Color: [3]uint8{0xA7, 0x9E, 0x82}, Label: "Tan"},
		{Color: [3]uint8{0x06, 0x92, 0x4D}, Label: "Green"},
		{Color: [3]uint8{0xD5, 0xD7, 0x01}, Label: "Lime Green"},
		{Color: [3]uint8{0x4E, 0x74, 0x2D}, Label: "Jungle Green"},
		{Color: [3]uint8{0x94, 0x89, 0x02}, Label: "Olive Green"},
		{Color: [3]uint8{0x57, 0x5B, 0x54}, Label: "Dark Olive Drab"},
		{Color: [3]uint8{0x00, 0x37, 0x76}, Label: "Blue"},
		{Color: [3]uint8{0x00, 0x66, 0xD9}, Label: "Azure Blue"},
		{Color: [3]uint8{0x48, 0x7B, 0xA2}, Label: "Stone Blue"},
		{Color: [3]uint8{0x5E, 0xBD, 0xDB}, Label: "Aqua Blue"},
		{Color: [3]uint8{0x4C, 0xC0, 0xC7}, Label: "Polymaker Teal"},
		{Color: [3]uint8{0x6C, 0x47, 0xB2}, Label: "Purple"},
		{Color: [3]uint8{0x48, 0x52, 0x59}, Label: "Dark Grey"},
		{Color: [3]uint8{0x61, 0x64, 0x69}, Label: "Steel Grey"},
		{Color: [3]uint8{0x8C, 0x90, 0x99}, Label: "Grey"},
		{Color: [3]uint8{0xD9, 0xDF, 0xE5}, Label: "Cold White"},
		{Color: [3]uint8{0xEB, 0xF7, 0xFF}, Label: "White"},
	}
}
