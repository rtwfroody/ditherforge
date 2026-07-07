package settings

import (
	"strconv"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// ToOptions converts a Settings (the persisted/JSON representation) into
// the pipeline.Options the engine consumes. This is the single Go
// equivalent of the frontend's old buildOpts(): both the GUI and the CLI
// route through here so a given settings JSON produces identical output
// regardless of entry point.
//
// mgr resolves the named inventory collection into concrete colors+TDs;
// pass nil to skip inventory resolution (only the locked palette slots
// are used in that case). Runtime-only Options fields (Force, ReloadSeq)
// are NOT set here — the caller layers them on after conversion.
func ToOptions(s Settings, mgr *collection.Manager) (pipeline.Options, error) {
	opts := pipeline.Options{
		Input:                                s.InputFile,
		NumColors:                            len(s.ColorSlots),
		Printer:                              s.Printer,
		BaseColorMaterialXTileMM:             s.BaseMaterialXTileMM,
		BaseColorMaterialXTriplanarSharpness: s.BaseMaterialXTriplanarSharpness,
		Brightness:                           float32(s.Brightness),
		Contrast:                             float32(s.Contrast),
		Saturation:                           float32(s.Saturation),
		Dither:                               s.Dither,
		RiemersmaInputBias:                   s.RiemersmaBias,
		BlueNoiseTolerance:                   s.BlueNoiseTol,
		NoMerge:                              s.NoMerge,
		NoCellMerge:                          s.NoCellMerge,
		NoSimplify:                           s.NoSimplify,
		HonorTD:                              s.HonorTD,
		ColorAwareCells:                      s.ColorAwareCells,
		ColorRegionContrast:                  s.ColorRegionContrast,
		RegionDither:                         s.RegionDither,
		RegionDitherDeltaE:                   s.RegionDitherDeltaE,
		RejectColorOutliers:                  s.RejectColorOutliers,
		ShowSampledColors:                    s.ShowSampledColors,
		Stats:                                s.Stats,
		ColorSnap:                            s.ColorSnap,
		AlphaWrap:                            s.AlphaWrap,
		Layer0AdhesionXYScale:                float32(s.Layer0AdhesionXYScale),
		UpperLayerXYScale:                    float32(s.UpperLayerXYScale),
		TDModel:                              s.TDModel,
		// ShellThicknessMM is left zero here: it is derived from the printer
		// process profile in applyFractionalOptions, not carried from settings.
		InfillColor: parseHexRGB(s.InfillColor, [3]uint8{255, 255, 255}),
	}

	// ObjectIndex: nil → -1 (all objects), matching the frontend.
	if s.ObjectIndex != nil {
		opts.ObjectIndex = *s.ObjectIndex
	} else {
		opts.ObjectIndex = -1
	}

	// Size / scale. sizeMode "scale" uses ScaleValue as a multiplier;
	// "size" uses SizeValue as the target max-extent in mm.
	opts.Scale = 1.0
	if s.SizeMode == "scale" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s.ScaleValue), 32); err == nil && v != 0 {
			opts.Scale = float32(v)
		}
	} else if s.SizeMode == "size" && strings.TrimSpace(s.SizeValue) != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s.SizeValue), 32); err == nil {
			sz := float32(v)
			opts.Size = &sz
		}
	}

	// Nozzle / layer height / alpha-wrap params are stored as strings in
	// the UI; parse with the same fallbacks the frontend used.
	opts.NozzleDiameter = parseF32(s.NozzleDiameter, 0.4)
	opts.LayerHeight = parseF32(s.LayerHeight, 0.2)
	opts.AlphaWrapAlpha = parseF32(s.AlphaWrapAlpha, 0)
	opts.AlphaWrapOffset = parseF32(s.AlphaWrapOffset, 0)

	// Base color vs MaterialX texture. The frontend always sends the
	// solid hex; the pipeline ignores it whenever MaterialX is set.
	if s.BaseColor != nil {
		opts.BaseColor = s.BaseColor.Hex
	}
	if s.BaseColorMode == "texture" {
		opts.BaseColorMaterialX = s.BaseMaterialXPath
	}

	// Locked colors (palette slots; nil slots are "unlocked").
	for _, slot := range s.ColorSlots {
		if slot == nil {
			continue
		}
		opts.LockedColors = append(opts.LockedColors, slot.Hex)
		opts.LockedTDs = append(opts.LockedTDs, tdOr(slot.TD))
	}

	// Inventory: resolve the named collection into colors/labels/TDs. A
	// named-but-missing collection (or a nil mgr) degrades to an empty
	// inventory and the run proceeds on the locked palette slots — matching
	// the old GUI buildOpts, which read the frontend's already-resolved
	// color mirror and never failed here. mgr.Get("") reports not-found, so
	// no inventory name also yields an empty list.
	if mgr != nil {
		if col, ok := mgr.Get(s.InventoryCollection); ok {
			for _, e := range col.Entries {
				opts.InventoryColors = append(opts.InventoryColors, e.Color)
				opts.InventoryLabels = append(opts.InventoryLabels, e.Label)
				opts.InventoryTDs = append(opts.InventoryTDs, tdOr(e.TD))
			}
		}
	}

	// Warp pins: drop any with malformed hex (mirrors the frontend filter).
	for _, p := range s.WarpPins {
		if !validHex(p.SourceHex) || !validHex(p.TargetHex) {
			continue
		}
		opts.WarpPins = append(opts.WarpPins, pipeline.WarpPin{
			SourceHex: p.SourceHex,
			TargetHex: p.TargetHex,
			Sigma:     p.Sigma,
		})
	}

	// Stickers.
	for _, st := range s.Stickers {
		opts.Stickers = append(opts.Stickers, pipeline.Sticker{
			ImagePath: st.ImagePath,
			Center:    st.Center,
			Normal:    st.Normal,
			Up:        st.Up,
			Scale:     st.Scale,
			Rotation:  st.Rotation,
			MaxAngle:  st.MaxAngle,
			Mode:      st.Mode,
		})
	}

	// Split.
	opts.Split = pipeline.SplitSettings{
		Enabled:          s.SplitEnabled,
		Axis:             s.SplitAxis,
		Offset:           s.SplitOffset,
		TiltADeg:         s.SplitTiltA,
		TiltBDeg:         s.SplitTiltB,
		ConnectorStyle:   s.SplitConnectorStyle,
		ConnectorCount:   s.SplitConnectorCount,
		ConnectorDiamMM:  s.SplitConnectorDiamMM,
		ConnectorDepthMM: s.SplitConnectorDepthMM,
		ClearanceMM:      s.SplitClearanceMM,
		Orientation:      [2]string{s.SplitOrientationA, s.SplitOrientationB},
	}

	return opts, nil
}

// parseF32 parses s (trimmed) as a float32. It returns def when s fails to
// parse OR parses to zero, mirroring the frontend's `parseFloat(x) || def`
// idiom (0 is falsy in JS, so a "0" entry fell back to the default).
// Callers whose default is itself 0 (the alpha-wrap "auto" knobs) get 0
// either way.
func parseF32(s string, def float32) float32 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 32); err == nil && v != 0 {
		return float32(v)
	}
	return def
}

// tdOr returns td when positive, else the default opaque TD (mirrors the
// frontend's `td || 1`).
func tdOr(td float32) float32 {
	if td > 0 {
		return td
	}
	return palette.DefaultTD
}

// parseHexRGB parses a "#RRGGBB" string into an sRGB triple, returning def on
// empty or malformed input (used for the layered TD model's infill color).
func parseHexRGB(s string, def [3]uint8) [3]uint8 {
	s = strings.TrimSpace(s)
	if !validHex(s) {
		return def
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return def
	}
	return [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

// validHex reports whether s is a "#RRGGBB" hex color.
func validHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
