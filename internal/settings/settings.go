// Package settings defines the persisted JSON settings format shared by
// the GUI and the CLI, plus the single conversion from those settings to
// pipeline.Options (see options.go). Making this the one canonical format
// means a given .json file produces identical output regardless of which
// front end loads it.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// File is the JSON structure written to/read from .json settings files.
type File struct {
	DitherForge Meta     `json:"_ditherforge"`
	Settings    Settings `json:"settings"`
}

// Meta contains metadata about the settings file.
type Meta struct {
	URL     string `json:"url"`
	Version string `json:"version"`
	// SizeRelativeUnits marks the fraction-of-extent format: every file Save
	// writes sets it true, meaning the size-relative fields (Split.Offset,
	// Stickers[].Center, Stickers[].Scale, BaseMaterialXTileMM) are stored as
	// a fraction of the scaled model's max extent. Its ABSENCE marks a legacy
	// file whose those fields are absolute mm, so Load reports it as legacy.
	//
	// Presence-based detection rather than a version comparison on purpose:
	// the on-disk version string has proven unreliable (committed fixtures
	// carry versions like "0.9.27" that the app never shipped), so semver
	// ordering can't safely decide the format.
	SizeRelativeUnits bool `json:"sizeRelativeUnits"`
}

// StickerSetting is the JSON representation of a sticker for settings persistence.
type StickerSetting struct {
	ImagePath string     `json:"imagePath"`
	Center    [3]float64 `json:"center"`
	Normal    [3]float64 `json:"normal"`
	Up        [3]float64 `json:"up"`
	Scale     float64    `json:"scale"`
	Rotation  float64    `json:"rotation"`
	MaxAngle  float64    `json:"maxAngle,omitempty"`
	Mode      string     `json:"mode,omitempty"`
}

// WarpPinSetting is the JSON representation of a color warp pin.
type WarpPinSetting struct {
	SourceHex   string  `json:"sourceHex"`
	TargetHex   string  `json:"targetHex"`
	TargetLabel string  `json:"targetLabel,omitempty"`
	Sigma       float64 `json:"sigma"`
}

// ColorSlotSetting is the JSON representation of a color slot.
type ColorSlotSetting struct {
	Hex        string  `json:"hex"`
	Label      string  `json:"label,omitempty"`
	Collection string  `json:"collection,omitempty"`
	TD         float32 `json:"td,omitempty"` // transmission distance in mm; 0 = default opaque
}

// Settings contains all user-configurable settings.
//
// The save/load round-trip is defended in three layers, each
// addressing a distinct failure mode that would otherwise cause
// silent data loss:
//
//  1. Frontend → Go field-name drift is caught at TypeScript
//     compile time by a guard in App.svelte that asserts
//     `keyof serializeSettings ⊆ keyof settings.Settings`. A frontend-
//     only key never reaches save without failing svelte-check.
//
//  2. Go-internal field drops (unexported field, missing/typo'd
//     json tag, ...) are caught by TestSaveLoadRoundTripPreservesAllFields,
//     which marshals a fully-populated Settings, unmarshals into
//     a zero File, and asserts DeepEqual: any field whose JSON
//     encoding doesn't round-trip surfaces as zero post-unmarshal
//     and the DeepEqual fails. A reflection pre-flight refuses to
//     run unless every exported top-level field of Settings is
//     non-zero in nonDefaultSettings.
//
//  3. Legacy files predating a field would otherwise unmarshal that
//     field as the Go zero value, which the frontend can't
//     distinguish from "user set it to zero". Load pre-populates
//     with Default() before unmarshalling, so a missing key keeps
//     its application default.
type Settings struct {
	InputFile string `json:"inputFile,omitempty"`
	// ObjectIndex is a pointer so old settings files (no field) decode as nil;
	// the frontend maps nil → -1 ("all objects"), which differs from 0 ("first object").
	ObjectIndex    *int              `json:"objectIndex,omitempty"`
	SizeMode       string            `json:"sizeMode"`
	SizeValue      string            `json:"sizeValue"`
	ScaleValue     string            `json:"scaleValue"`
	Printer        string            `json:"printer,omitempty"`
	NozzleDiameter string            `json:"nozzleDiameter"`
	LayerHeight    string            `json:"layerHeight"`
	BaseColor      *ColorSlotSetting `json:"baseColor,omitempty"`
	// BaseMaterialXPath is the on-disk path of the user-selected .mtlx
	// file or .zip archive. The pipeline reads the file at run time —
	// settings only stores the path.
	BaseMaterialXPath               string  `json:"baseMaterialXPath,omitempty"`
	BaseMaterialXTileMM             float64 `json:"baseMaterialXTileMM,omitempty"`
	BaseMaterialXTriplanarSharpness float64 `json:"baseMaterialXTriplanarSharpness,omitempty"`
	// BaseColorMode is "solid" or "texture" — UI mode toggle that
	// decides which of (BaseColor, BaseMaterialXPath) is sent to the
	// pipeline.
	BaseColorMode       string              `json:"baseColorMode,omitempty"`
	ColorSlots          []*ColorSlotSetting `json:"colorSlots"`
	InventoryCollection string              `json:"inventoryCollection"`
	Brightness          float64             `json:"brightness"`
	Contrast            float64             `json:"contrast"`
	Saturation          float64             `json:"saturation"`
	WarpPins            []WarpPinSetting    `json:"warpPins"`
	Stickers            []StickerSetting    `json:"stickers,omitempty"`
	Dither              string              `json:"dither"`
	RiemersmaBias       float64             `json:"riemersmaBias"`
	BlueNoiseTol        float64             `json:"blueNoiseTol"`
	ColorSnap           float64             `json:"colorSnap"`
	NoMerge             bool                `json:"noMerge"`
	NoCellMerge         bool                `json:"noCellMerge"`
	NoSimplify          bool                `json:"noSimplify"`
	HonorTD             bool                `json:"honorTD"`
	// TDModel selects the translucency-compensation model used when HonorTD is
	// on: "" (or "area") = legacy opacity-weighted area mix; "layered" = the
	// infill-aware N-crossing model. Default "" (area). See
	// docs/td-translucency-model.md.
	TDModel string `json:"tdModel,omitempty"`
	// InfillColor is the "#RRGGBB" color of the infill filament the layered TD
	// model blends translucent colors toward. Default "#FFFFFF". The layered
	// model's shell thickness is not a setting: it is derived from the selected
	// printer's process profile (see pipeline.Options.ShellThicknessMM).
	InfillColor         string  `json:"infillColor,omitempty"`
	ColorAwareCells     bool    `json:"colorAwareCells"`
	ColorRegionContrast float64 `json:"colorRegionContrast"`
	// RegionDither (advanced) confines the dither to colour regions so a
	// grey region's diffused error can't bleed into an adjacent solid
	// black/white region. Independent of colorAwareCells; off by default.
	RegionDither       bool    `json:"regionDither"`
	RegionDitherDeltaE float64 `json:"regionDitherDeltaE"`
	// RejectColorOutliers discards stray minority colour samples when a
	// cell's interior samples are dominated (≥75% by ΔE cluster) by one
	// colour, so a lone sample straying across a colour boundary doesn't
	// pull the cell's averaged colour. On by default.
	RejectColorOutliers bool `json:"rejectColorOutliers"`
	Stats               bool `json:"stats"`
	ShowSampledColors   bool `json:"showSampledColors"`
	// MeshRepair selects the pre-clip mesh-repair method: "none" (use the
	// model as-is), "fwn" (winding-number remesh, see internal/fwnrepair),
	// or "alphawrap" (CGAL alpha wrap, see internal/alphawrap). Load
	// normalizes any other value to "none".
	MeshRepair string `json:"meshRepair"`
	// AlphaWrap is legacy-load-only: pre-MeshRepair files carry this bool.
	// Load migrates a true value to MeshRepair=="alphawrap" and clears it,
	// and Save omits it (omitempty) so re-saves drop the legacy key.
	AlphaWrap       bool   `json:"alphaWrap,omitempty"`
	AlphaWrapAlpha  string `json:"alphaWrapAlpha"`
	AlphaWrapOffset string `json:"alphaWrapOffset"`
	// FWN remesh per-axis grid-pitch overrides (mm; "" = auto — XY from
	// nozzle diameter, Z from layer height). Consulted only in "fwn" mode.
	FWNDetailXY string `json:"fwnDetailXY"`
	FWNDetailZ  string `json:"fwnDetailZ"`
	// Voxel-grid XY multipliers. See Default for the values missing-from-JSON
	// keys are filled with on load.
	Layer0AdhesionXYScale float64 `json:"layer0AdhesionXYScale"`
	UpperLayerXYScale     float64 `json:"upperLayerXYScale"`
	// Split panel state.
	SplitEnabled          bool    `json:"splitEnabled"`
	SplitAxis             int     `json:"splitAxis"`
	SplitOffset           float64 `json:"splitOffset"`
	SplitTiltA            float64 `json:"splitTiltA"` // tilt about in-plane U axis, degrees
	SplitTiltB            float64 `json:"splitTiltB"` // tilt about in-plane V axis, degrees
	SplitConnectorStyle   string  `json:"splitConnectorStyle"`
	SplitConnectorCount   int     `json:"splitConnectorCount"`
	SplitConnectorDiamMM  float64 `json:"splitConnectorDiamMM"`
	SplitConnectorDepthMM float64 `json:"splitConnectorDepthMM"`
	SplitClearanceMM      float64 `json:"splitClearanceMM"`
	SplitOrientationA     string  `json:"splitOrientationA"`
	SplitOrientationB     string  `json:"splitOrientationB"`
}

// Default returns the application-wide default Settings.
//
// Load pre-populates a Settings value with these defaults before
// unmarshalling the on-disk JSON. Go's encoding/json only overwrites
// fields the JSON explicitly contains, so any key missing from the file
// (e.g. an older settings file that pre-dates a feature) retains its
// default value here rather than collapsing to the Go zero value.
func Default() Settings {
	allObjects := -1
	return Settings{
		ObjectIndex:                     &allObjects,
		SizeMode:                        "size",
		SizeValue:                       "100",
		ScaleValue:                      "1.0",
		Printer:                         "snapmaker_u1",
		NozzleDiameter:                  "0.4",
		LayerHeight:                     "0.20",
		BaseMaterialXTileMM:             0.1, // fraction of model max extent (was 10 mm)
		BaseMaterialXTriplanarSharpness: 4,
		BaseColorMode:                   "solid",
		ColorSlots: []*ColorSlotSetting{
			nil, nil, nil, nil,
		},
		InventoryCollection:   "Inventory",
		Brightness:            0,
		Contrast:              0,
		Saturation:            0,
		WarpPins:              []WarpPinSetting{},
		Stickers:              []StickerSetting{},
		Dither:                "dlc-d30-p7",
		RiemersmaBias:         0.85,
		BlueNoiseTol:          20,
		ColorSnap:             5,
		NoMerge:               false,
		NoCellMerge:           false,
		NoSimplify:            false,
		HonorTD:               true,
		InfillColor:           "#FFFFFF",
		ColorAwareCells:       true,
		ColorRegionContrast:   20,
		RegionDither:          false,
		RegionDitherDeltaE:    20,
		RejectColorOutliers:   true,
		Stats:                 false,
		ShowSampledColors:     false,
		MeshRepair:            "none",
		AlphaWrap:             false,
		AlphaWrapAlpha:        "",
		AlphaWrapOffset:       "",
		FWNDetailXY:           "",
		FWNDetailZ:            "",
		Layer0AdhesionXYScale: 2,
		UpperLayerXYScale:     1.25,
		SplitEnabled:          false,
		SplitAxis:             2,
		SplitOffset:           0,
		SplitTiltA:            0,
		SplitTiltB:            0,
		SplitConnectorStyle:   "pegs",
		SplitConnectorCount:   0,
		SplitConnectorDiamMM:  3,
		SplitConnectorDepthMM: 2,
		SplitClearanceMM:      0.15,
		SplitOrientationA:     "original",
		SplitOrientationB:     "original",
	}
}

// transformPaths applies fn to every on-disk-asset path in the Settings
// struct. Centralized so adding a new path-typed field requires updating
// one place — both save (relativise) and load (resolve) route through here.
//
// In-memory contract: each path field is expected to be ABSOLUTE while
// held in Settings. Load resolves stored relative paths back to absolute
// on its way out, and the pipeline expects absolute paths.
func transformPaths(s *Settings, fn func(string) string) {
	s.InputFile = fn(s.InputFile)
	s.BaseMaterialXPath = fn(s.BaseMaterialXPath)
	for i := range s.Stickers {
		s.Stickers[i].ImagePath = fn(s.Stickers[i].ImagePath)
	}
}

// pathForSaving converts an in-memory absolute path into the form stored
// in a settings JSON file at jsonPath. Returns a relative path when the
// asset is in the same directory, a subdirectory, or one directory up
// from the JSON file; otherwise returns the absolute path unchanged.
func pathForSaving(jsonPath, p string) string {
	if p == "" {
		return p
	}
	if !filepath.IsAbs(p) {
		return p
	}
	absJSON, err := filepath.Abs(jsonPath)
	if err != nil {
		return p
	}
	jsonDir := filepath.Dir(absJSON)
	rel, err := filepath.Rel(jsonDir, p)
	if err != nil {
		return p
	}
	// Cap the "up" depth at one. Anything deeper would imply a project
	// layout we don't want to bake into settings files — fall back to
	// absolute. Counting only leading ".." components is sufficient
	// because filepath.Rel returns a Clean'd path.
	upCount := 0
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part != ".." {
			break
		}
		upCount++
	}
	if upCount > 1 {
		return p
	}
	return rel
}

// pathForLoading resolves a path from a settings JSON file at jsonPath
// back to the absolute form the in-memory state and pipeline use.
// Absolute or empty paths pass through unchanged.
func pathForLoading(jsonPath, p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	absJSON, err := filepath.Abs(jsonPath)
	if err != nil {
		return p
	}
	return filepath.Clean(filepath.Join(filepath.Dir(absJSON), p))
}

// Load reads and parses a settings file at path, resolving stored
// relative asset paths to absolute against the file's directory.
//
// Keys absent from the file retain their Default() value (so older files
// predating a field never observe Go zeros), and the file must carry the
// _ditherforge metadata or it is rejected.
//
// legacyAbsoluteUnits is true when the file predates the fraction-of-extent
// format (see fractionalUnitsVersion) and so stores the size-relative fields
// as absolute mm. The caller passes this to pipeline.Options.LegacyAbsoluteUnits
// (CLI) or surfaces it to the frontend (GUI) so the values are interpreted
// correctly rather than rescaled by the model extent.
func Load(path string) (s Settings, legacyAbsoluteUnits bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, false, fmt.Errorf("read settings: %w", err)
	}
	sf := File{Settings: Default()}
	if err := json.Unmarshal(data, &sf); err != nil {
		return Settings{}, false, fmt.Errorf("parse settings: %w", err)
	}
	if sf.DitherForge.URL == "" {
		return Settings{}, false, fmt.Errorf("not a DitherForge settings file (missing _ditherforge metadata)")
	}
	// Migrate legacy stickers that predate the Mode field.
	for i := range sf.Settings.Stickers {
		if sf.Settings.Stickers[i].Mode == "" {
			sf.Settings.Stickers[i].Mode = "unfold"
		}
	}
	// Migrate dither modes removed from the product: their benchmark
	// wins were artifacts/duplicates of surviving modes, or (in the
	// 2026-07 second cut, driven by the CSF perceptual election) they
	// were superseded by the promoted tuned variants dlc-d30-p7 and
	// bn-adapt-5. Old files naming a removed mode silently load as the
	// nearest surviving mode.
	switch sf.Settings.Dither {
	case "riemersma-pair":
		sf.Settings.Dither = "riemersma"
	case "dizzy-2hop", "dizzy-recover", "dizzy-corrected", "dizzy-local-corrected":
		sf.Settings.Dither = "dlc-d30-p7"
	case "blue-noise":
		sf.Settings.Dither = "bn-adapt-5"
	}
	// Migrate the legacy AlphaWrap bool to the three-way MeshRepair enum.
	// A pre-MeshRepair file has no meshRepair key (decodes as the Default()
	// "none") but may carry alphaWrap:true — map that to "alphawrap". Then
	// normalize any unrecognized value to "none", and always clear the
	// legacy bool so a re-save (which omits it via omitempty) drops the key.
	if (sf.Settings.MeshRepair == "" || sf.Settings.MeshRepair == "none") && sf.Settings.AlphaWrap {
		sf.Settings.MeshRepair = "alphawrap"
	}
	switch sf.Settings.MeshRepair {
	case "none", "fwn", "alphawrap":
		// allowed
	default:
		sf.Settings.MeshRepair = "none"
	}
	sf.Settings.AlphaWrap = false
	transformPaths(&sf.Settings, func(p string) string { return pathForLoading(path, p) })
	return sf.Settings, !sf.DitherForge.SizeRelativeUnits, nil
}

// Save writes settings to path, relativising on-disk asset paths against
// the JSON file's directory where possible. If the file already exists
// and is not a DitherForge settings file, it refuses to overwrite it.
// The caller's Settings is not mutated.
func Save(path string, s Settings) error {
	if data, err := os.ReadFile(path); err == nil {
		var existing File
		if jsonErr := json.Unmarshal(data, &existing); jsonErr != nil || existing.DitherForge.URL == "" {
			return fmt.Errorf("refusing to overwrite %s: not a DitherForge settings file", filepath.Base(path))
		}
	}
	// Copy stickers so transformPaths doesn't mutate the caller's slice.
	// Other path fields are scalars, naturally copied by value receive.
	s.Stickers = append([]StickerSetting(nil), s.Stickers...)
	transformPaths(&s, func(p string) string { return pathForSaving(path, p) })
	sf := File{
		DitherForge: Meta{
			URL:               "https://github.com/rtwfroody/ditherforge",
			Version:           pipeline.Version,
			SizeRelativeUnits: true,
		},
		Settings: s,
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
