package pipeline

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"math"
	"os"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// StageID identifies a pipeline stage.
type StageID int

const (
	StageLoad    StageID = iota
	StageSticker         // builds decals from mesh, before voxelization
	StageVoxelize
	StageDecimate
	StageColorAdjust
	StageColorWarp
	StagePalette
	StageDither
	StageClip
	StageMerge
	numStages
)

// StageCache holds per-stage cached outputs so that only invalidated stages
// re-run when settings change.
type StageCache struct {
	stages [numStages]*cachedStage
}

type cachedStage struct {
	key    uint64
	output any
}

// NewStageCache returns an empty stage cache.
func NewStageCache() *StageCache {
	return &StageCache{}
}

// Per-stage output types.

type loadOutput struct {
	Model        *loader.LoadedModel
	InputMesh    *MeshData
	PreviewScale float32 // scale factor to convert pipeline coords back to preview coords
}

type voxelizeOutput struct {
	Cells         []voxel.ActiveCell
	CellAssignMap map[voxel.CellKey]int
	MinV          [3]float32
	Layer0Size    float32
	UpperSize     float32
	LayerH        float32
	TwoGrid       bool
}

type stickerOutput struct {
	Decals []*voxel.StickerDecal
	Adj    *voxel.TriAdjacency // cached for potential reuse
}

type colorAdjustOutput struct {
	Cells []voxel.ActiveCell
}

type colorWarpOutput struct {
	Cells []voxel.ActiveCell
}

type decimateOutput struct {
	DecimModel *loader.LoadedModel
}

type paletteOutput struct {
	Palette       [][3]uint8
	PaletteLabels []string           // parallel to Palette; label from inventory (empty for locked/computed)
	Cells         []voxel.ActiveCell // copy with snapped colors
}

type ditherOutput struct {
	Assignments     []int32
	PatchMap        map[voxel.CellKey]int
	NumPatches      int
	PatchAssignment []int32
}

type clipOutput struct {
	ShellVerts       [][3]float32
	ShellFaces       [][3]uint32
	ShellAssignments []int32
}

// mergeOutput has the same structure as clipOutput. When NoMerge is true,
// the slices alias the clip output (safe because nothing mutates them after
// caching).
type mergeOutput struct {
	ShellVerts       [][3]float32
	ShellFaces       [][3]uint32
	ShellAssignments []int32
}

// --- Per-stage settings structs for cache key computation ---
//
// Each struct contains exactly the Options fields that affect that stage.
// The stageKey function hashes the struct using binary.Write, so the key
// is type-safe and free of format-string ambiguities.

type loadSettings struct {
	Input     string
	Scale     float32
	HasSize   bool
	Size      float32
	ReloadSeq int64
}

type voxelizeSettings struct {
	NozzleDiameter float32
	LayerHeight    float32
	UniformGrid    bool
}

type stickerSettings struct {
	Stickers []Sticker
}

type colorAdjustSettings struct {
	Brightness float32
	Contrast   float32
	Saturation float32
}

type colorWarpSettings struct {
	WarpPins []WarpPin
}

type decimateSettings struct {
	NoSimplify bool
}

type paletteSettings struct {
	NumColors         int
	LockedColors      string   // joined for hashing
	InventoryFile     string
	InventoryContents string   // file contents for hashing; empty if no file
	InventoryColors   [][3]uint8
	InventoryLabels   []string
	ColorSnap         float64
}

type ditherSettings struct {
	Dither string
}

// clipSettings has no fields: clip is invalidated only by dependency cascade.
type clipSettings struct{}

type mergeSettings struct {
	NoMerge bool
}

// hashSettings computes an FNV-1a hash for a stage's settings.
func writeString(h hash.Hash64, s string) {
	// Length-prefix to avoid ambiguity between e.g. "ab"+"c" and "a"+"bc".
	binary.Write(h, binary.LittleEndian, uint32(len(s)))
	h.Write([]byte(s))
}

func writeFloat32(h hash.Hash64, f float32) {
	binary.Write(h, binary.LittleEndian, math.Float32bits(f))
}

func writeFloat64(h hash.Hash64, f float64) {
	binary.Write(h, binary.LittleEndian, math.Float64bits(f))
}

func writeBool(h hash.Hash64, b bool) {
	v := byte(0)
	if b {
		v = 1
	}
	h.Write([]byte{v})
}

func writeInt(h hash.Hash64, i int) {
	binary.Write(h, binary.LittleEndian, int64(i))
}

func settingsForStage(stage StageID, opts Options) any {
	switch stage {
	case StageLoad:
		s := loadSettings{Input: opts.Input, Scale: opts.Scale, ReloadSeq: opts.ReloadSeq}
		if opts.Size != nil {
			s.HasSize = true
			s.Size = *opts.Size
		}
		return s
	case StageVoxelize:
		return voxelizeSettings{NozzleDiameter: opts.NozzleDiameter, LayerHeight: opts.LayerHeight, UniformGrid: opts.UniformGrid}
	case StageSticker:
		return stickerSettings{Stickers: opts.Stickers}
	case StageColorAdjust:
		return colorAdjustSettings{Brightness: opts.Brightness, Contrast: opts.Contrast, Saturation: opts.Saturation}
	case StageColorWarp:
		return colorWarpSettings{WarpPins: opts.WarpPins}
	case StageDecimate:
		return decimateSettings{NoSimplify: opts.NoSimplify}
	case StagePalette:
		var contents string
		if opts.InventoryFile != "" {
			if data, err := os.ReadFile(opts.InventoryFile); err == nil {
				contents = string(data)
			}
		}
		return paletteSettings{
			NumColors:         opts.NumColors,
			LockedColors:      strings.Join(opts.LockedColors, ","),
			InventoryFile:     opts.InventoryFile,
			InventoryContents: contents,
			InventoryColors:   opts.InventoryColors,
			InventoryLabels:   opts.InventoryLabels,
			ColorSnap:         opts.ColorSnap,
		}
	case StageDither:
		return ditherSettings{Dither: opts.Dither}
	case StageClip:
		return clipSettings{}
	case StageMerge:
		return mergeSettings{NoMerge: opts.NoMerge}
	}
	return nil
}

// stageKey computes an FNV-1a hash of the settings that affect a given stage.
func stageKey(stage StageID, opts Options) uint64 {
	h := fnv.New64a()
	s := settingsForStage(stage, opts)
	switch v := s.(type) {
	case loadSettings:
		writeString(h, v.Input)
		writeFloat32(h, v.Scale)
		writeBool(h, v.HasSize)
		writeFloat32(h, v.Size)
		binary.Write(h, binary.LittleEndian, v.ReloadSeq)
	case voxelizeSettings:
		writeFloat32(h, v.NozzleDiameter)
		writeFloat32(h, v.LayerHeight)
		writeBool(h, v.UniformGrid)
	case stickerSettings:
		writeInt(h, len(v.Stickers))
		for _, s := range v.Stickers {
			writeString(h, s.ImagePath)
			// Include image file mod time so changes to the PNG invalidate cache.
			if info, err := os.Stat(s.ImagePath); err == nil {
				binary.Write(h, binary.LittleEndian, info.ModTime().UnixNano())
			}
			for _, c := range s.Center {
				writeFloat64(h, c)
			}
			for _, n := range s.Normal {
				writeFloat64(h, n)
			}
			for _, u := range s.Up {
				writeFloat64(h, u)
			}
			writeFloat64(h, s.Scale)
			writeFloat64(h, s.Rotation)
			writeFloat64(h, s.MaxAngle)
		}
	case colorAdjustSettings:
		writeFloat32(h, v.Brightness)
		writeFloat32(h, v.Contrast)
		writeFloat32(h, v.Saturation)
	case colorWarpSettings:
		writeInt(h, len(v.WarpPins))
		for _, p := range v.WarpPins {
			writeString(h, p.SourceHex)
			writeString(h, p.TargetHex)
			writeFloat64(h, p.Sigma)
		}
	case decimateSettings:
		writeBool(h, v.NoSimplify)
	case paletteSettings:
		writeInt(h, v.NumColors)
		writeString(h, v.LockedColors)
		writeString(h, v.InventoryFile)
		writeString(h, v.InventoryContents)
		writeInt(h, len(v.InventoryColors))
		for _, c := range v.InventoryColors {
			h.Write(c[:])
		}
		// Labels are length-prefixed strings, so a shorter InventoryLabels
		// slice produces a different hash than a longer one even without an
		// explicit count. This is intentional — label count tracks color count.
		for _, l := range v.InventoryLabels {
			writeString(h, l)
		}
		writeFloat64(h, v.ColorSnap)
	case ditherSettings:
		writeString(h, v.Dither)
	case clipSettings:
		// No independent settings.
	case mergeSettings:
		writeBool(h, v.NoMerge)
	}
	return h.Sum64()
}

// Invalidate computes new keys for each stage and returns the earliest stage
// whose key changed (meaning it and everything after must re-run). If all
// keys match, returns numStages (nothing to re-run).
func (c *StageCache) Invalidate(opts Options) StageID {
	for s := StageID(0); s < numStages; s++ {
		newKey := stageKey(s, opts)
		if c.stages[s] == nil || c.stages[s].key != newKey {
			// Clear this stage and everything after.
			for j := s; j < numStages; j++ {
				c.stages[j] = nil
			}
			return s
		}
	}
	return numStages
}

// Typed getters.

func (c *StageCache) getLoad() *loadOutput {
	if c.stages[StageLoad] == nil {
		return nil
	}
	return c.stages[StageLoad].output.(*loadOutput)
}

func (c *StageCache) getVoxelize() *voxelizeOutput {
	if c.stages[StageVoxelize] == nil {
		return nil
	}
	return c.stages[StageVoxelize].output.(*voxelizeOutput)
}

func (c *StageCache) getSticker() *stickerOutput {
	if c.stages[StageSticker] == nil {
		return nil
	}
	return c.stages[StageSticker].output.(*stickerOutput)
}

func (c *StageCache) getColorAdjust() *colorAdjustOutput {
	if c.stages[StageColorAdjust] == nil {
		return nil
	}
	return c.stages[StageColorAdjust].output.(*colorAdjustOutput)
}

func (c *StageCache) getColorWarp() *colorWarpOutput {
	if c.stages[StageColorWarp] == nil {
		return nil
	}
	return c.stages[StageColorWarp].output.(*colorWarpOutput)
}

func (c *StageCache) getDecimate() *decimateOutput {
	if c.stages[StageDecimate] == nil {
		return nil
	}
	return c.stages[StageDecimate].output.(*decimateOutput)
}

func (c *StageCache) getPalette() *paletteOutput {
	if c.stages[StagePalette] == nil {
		return nil
	}
	return c.stages[StagePalette].output.(*paletteOutput)
}

func (c *StageCache) getDither() *ditherOutput {
	if c.stages[StageDither] == nil {
		return nil
	}
	return c.stages[StageDither].output.(*ditherOutput)
}

func (c *StageCache) getClip() *clipOutput {
	if c.stages[StageClip] == nil {
		return nil
	}
	return c.stages[StageClip].output.(*clipOutput)
}

func (c *StageCache) getMerge() *mergeOutput {
	if c.stages[StageMerge] == nil {
		return nil
	}
	return c.stages[StageMerge].output.(*mergeOutput)
}

// Typed setter.

func (c *StageCache) setStage(stage StageID, key uint64, output any) {
	c.stages[stage] = &cachedStage{key: key, output: output}
}
