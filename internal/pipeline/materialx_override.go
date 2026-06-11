package pipeline

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Atlas patches are sized per-triangle based on the triangle's
// world-space extent: tiny triangles get 2×2 patches (one effective
// sample), large flat triangles get 64×64 (~4k samples) so the
// MaterialX texture stays sharp on broad regions of the model.
// Tier sizes are powers of 2 in increasing order.
//
// The classifier assigns tier t such that the triangle's longest
// edge is no more than tierSizes[t] * targetPixelMM, i.e. the patch
// has at least one texel per targetPixelMM along the longest edge.
// Triangles longer than the largest tier's range fall into that
// tier (undersampled but bounded memory).
var baseColorAtlasTierSizes = []int{2, 4, 8, 16, 32, 64}

// Approximate desired sample density along a triangle's longest
// edge. Half the typical FDM nozzle diameter (0.2 mm vs 0.4) so the
// preview resolves features at finer than the dithered output's
// voxel resolution. Smaller values blow up atlas memory; larger
// values blur fine detail.
const baseColorAtlasTargetPixelMM = 0.2

// materialxOverride adapts a materialx.Sampler to voxel.BaseColorOverride.
//
// For purely position-driven graphs (procedural marble, brick, etc.)
// the adapter forwards a single SampleAt call with the world-space mm
// position scaled by 1/tileMM. For graphs that consume UVs (image-
// backed PBR packs) it triplanar-projects: three SampleAt calls along
// the YZ, XZ, and XY planes, blended by |normal|^sharpness. This gives
// untextured meshes a continuous, seam-light texture without UV
// authoring.
//
// All fields are immutable after construction. Reentrant by virtue of
// the underlying Sampler being reentrant.
type materialxOverride struct {
	sampler   materialx.Sampler
	invTileMM float64
	useUV     bool
	sharpness float64
}

// SampleBaseColor implements voxel.BaseColorOverride.
func (m *materialxOverride) SampleBaseColor(ctx voxel.BaseColorContext) [3]uint8 {
	pos := [3]float64{
		float64(ctx.Pos[0]) * m.invTileMM,
		float64(ctx.Pos[1]) * m.invTileMM,
		float64(ctx.Pos[2]) * m.invTileMM,
	}
	if !m.useUV {
		// Procedural graphs ignore UV — one call is enough.
		return rgbToBytes(m.sampler.SampleAt(materialx.SampleContext{Pos: pos}))
	}
	return rgbToBytes(m.triplanar(pos, ctx.Normal))
}

// triplanar runs the underlying sampler three times against the three
// axis-aligned planes (YZ for X-facing surfaces, XZ for Y, XY for Z)
// and blends by the normal-derived weights. Sharpness controls how
// abruptly the projection switches across axis transitions: 1 is a
// soft cosine-weighted blend, higher values approach a hard box map.
//
// Each plane's U coordinate is multiplied by sign(normal.axis) so a
// face's UV traverses the same direction whether its normal points
// along +axis or -axis. Without this, a directional texture (text,
// arrows) would render mirrored across opposite-facing parallel
// faces. For direction-free textures (cobblestone, marble) the flip
// is invisible.
func (m *materialxOverride) triplanar(pos [3]float64, normal [3]float32) [3]float64 {
	signX := signOrPos(float64(normal[0]))
	signY := signOrPos(float64(normal[1]))
	signZ := signOrPos(float64(normal[2]))
	nx := math.Abs(float64(normal[0]))
	ny := math.Abs(float64(normal[1]))
	nz := math.Abs(float64(normal[2]))

	sharp := m.sharpness
	if sharp <= 0 {
		sharp = 4
	}
	wx := math.Pow(nx, sharp)
	wy := math.Pow(ny, sharp)
	wz := math.Pow(nz, sharp)
	sum := wx + wy + wz
	if sum < 1e-12 {
		// Degenerate normal — average the three planar samples
		// equally so a degenerate face renders with the same
		// "everywhere" pattern instead of popping to one of the three
		// projections.
		wx, wy, wz = 1.0/3, 1.0/3, 1.0/3
	} else {
		wx /= sum
		wy /= sum
		wz /= sum
	}

	var out [3]float64
	if wx > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[1] * signX, pos[2]},
		})
		out[0] += c[0] * wx
		out[1] += c[1] * wx
		out[2] += c[2] * wx
	}
	if wy > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0] * signY, pos[2]},
		})
		out[0] += c[0] * wy
		out[1] += c[1] * wy
		out[2] += c[2] * wy
	}
	if wz > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0] * signZ, pos[1]},
		})
		out[0] += c[0] * wz
		out[1] += c[1] * wz
		out[2] += c[2] * wz
	}
	return out
}

// classifyAtlasTier picks the smallest tier (in baseColorAtlasTierSizes)
// whose patch size N is at least lengthMM/baseColorAtlasTargetPixelMM,
// i.e. the patch has roughly one texel per target sample density along
// the queried axis. Triangles longer than the largest tier get clamped
// to that tier (undersampled but bounded memory).
func classifyAtlasTier(lengthMM float32) int {
	needed := math.Ceil(float64(lengthMM) / baseColorAtlasTargetPixelMM)
	for t, N := range baseColorAtlasTierSizes {
		if float64(N) >= needed {
			return t
		}
	}
	return len(baseColorAtlasTierSizes) - 1
}

// faceLayout records the per-face atlas geometry: the patch is
// aligned so its X axis runs along the triangle's longest edge, with
// width sized to the bbox extent in that direction and height sized
// to the third vertex's perpendicular distance. Long thin triangles
// thus get wide-but-short patches instead of square ones, avoiding
// the wasted memory and (at fixed budget) blurry result of square
// binning by area alone.
//
// AIdx/BIdx/CIdx are the face-local indices (0/1/2) re-ordered so
// that the longest edge runs from vertex A to vertex B; vertex C is
// the third vertex (potentially with a foot-of-perpendicular outside
// [A, B] for obtuse triangles, hence the bbox-extending sMin/sMax).
type faceLayout struct {
	WT, HT           uint8   // tier indices (into baseColorAtlasTierSizes)
	AIdx, BIdx, CIdx uint8   // face-local vertex re-ordering
	sMin, sMax       float32 // 1D bbox along the longest-edge axis (mm)
	hC               float32 // perpendicular distance from C to line AB (mm)
}

// computeFaceLayout sets up the bbox-aligned 2D parameterization for
// face fi. Returns AIdx/BIdx/CIdx such that AB is the longest edge.
func computeFaceLayout(model *loader.LoadedModel, fi int) faceLayout {
	f := model.Faces[fi]
	v := model.Vertices
	d2 := func(p, q [3]float32) float32 {
		dx := p[0] - q[0]
		dy := p[1] - q[1]
		dz := p[2] - q[2]
		return dx*dx + dy*dy + dz*dz
	}
	e01sq := d2(v[f[0]], v[f[1]])
	e12sq := d2(v[f[1]], v[f[2]])
	e20sq := d2(v[f[2]], v[f[0]])
	var aIdx, bIdx, cIdx uint8
	switch {
	case e01sq >= e12sq && e01sq >= e20sq:
		aIdx, bIdx, cIdx = 0, 1, 2 // longest edge: v0–v1
	case e12sq >= e20sq:
		aIdx, bIdx, cIdx = 1, 2, 0 // longest edge: v1–v2
	default:
		aIdx, bIdx, cIdx = 2, 0, 1 // longest edge: v2–v0
	}
	A := v[f[aIdx]]
	B := v[f[bIdx]]
	C := v[f[cIdx]]
	abx, aby, abz := B[0]-A[0], B[1]-A[1], B[2]-A[2]
	abLen := float32(math.Sqrt(float64(abx*abx + aby*aby + abz*abz)))
	if abLen < 1e-6 {
		// Degenerate triangle (all three vertices coincide). Force
		// minimum extents so layout doesn't divide by zero.
		return faceLayout{WT: 0, HT: 0, AIdx: aIdx, BIdx: bIdx, CIdx: cIdx, sMin: 0, sMax: 1, hC: 1}
	}
	abHatX, abHatY, abHatZ := abx/abLen, aby/abLen, abz/abLen
	acx, acy, acz := C[0]-A[0], C[1]-A[1], C[2]-A[2]
	sC := acx*abHatX + acy*abHatY + acz*abHatZ
	perpX := acx - sC*abHatX
	perpY := acy - sC*abHatY
	perpZ := acz - sC*abHatZ
	hC := float32(math.Sqrt(float64(perpX*perpX + perpY*perpY + perpZ*perpZ)))
	if hC < 1e-6 {
		hC = 1e-3 // collinear; render as a thin sliver
	}
	sMin := float32(0)
	sMax := abLen
	if sC < sMin {
		sMin = sC
	}
	if sC > sMax {
		sMax = sC
	}
	wReal := sMax - sMin
	wT := classifyAtlasTier(wReal)
	hT := classifyAtlasTier(hC)
	return faceLayout{
		WT: uint8(wT), HT: uint8(hT),
		AIdx: aIdx, BIdx: bIdx, CIdx: cIdx,
		sMin: sMin, sMax: sMax, hC: hC,
	}
}

// bakeMaterialXAtlas evaluates the materialx sampler at per-triangle
// pixel grids and packs the results into a single image atlas.
//
// Each triangle gets a rectangular patch sized to its bbox in a
// frame aligned with its longest edge: width tier from the bbox's
// extent along the longest edge, height tier from the third vertex's
// perpendicular distance from that edge. Long thin triangles thus
// get wide-but-short patches (e.g. 32×4) instead of wasted square
// patches (32×32) — same sharpness on the long axis, much less
// memory.
//
// Within each (Wt, Ht) bucket, patches are grid-packed; buckets are
// stacked vertically in the atlas. Per-face-vertex UVs put each
// vertex at the texel center it was sampled at; GPU linear-filter
// sampling at any in-triangle UV reads the correctly pre-baked
// position.
//
// Bake-side, each pixel (i, j) in a patch maps to a 3D position via
// the longest-edge frame:
//
//	s = sMin + (i + 0.5 - 0.5) / (W-1) * (sMax - sMin)  (along AB)
//	t = (j + 0.5 - 0.5) / (H-1) * hC                    (perp from AB)
//	pos = A + s * abHat + t * perpHat
//
// Pixels outside the triangle's barycentric extent (on the obtuse-C
// side) extrapolate to well-defined positions; the GPU never samples
// them in-fragment.
//
// NoTextureMask gating mirrors bakeMaterialXBaseColor: textured
// faces skip the bake (their patches stay zeroed; the frontend
// renders those faces from their own textures).
//
// progress, when non-nil, is invoked with current sample count
// every ~1% so the GUI can render a bar; total = sum(W_b * H_b *
// count_b) over (Wt, Ht) buckets. Workers split faces; each writes
// a disjoint atlas region.
func bakeMaterialXAtlas(ctx context.Context, model *loader.LoadedModel, override voxel.BaseColorOverride, progressCB func(current int)) (*BaseColorAtlas, error) {
	if model == nil || len(model.Faces) == 0 {
		return nil, nil
	}
	nFaces := len(model.Faces)
	tiers := baseColorAtlasTierSizes
	nT := len(tiers)
	bucketIdx := func(wT, hT int) int { return wT*nT + hT }

	// Initial per-face layout. The demotion loop below mutates
	// WT/HT in place if the resulting atlas would exceed
	// `maxAtlasDim`, so layouts is computed once but counts/slots
	// are recomputed each pass.
	layouts := make([]faceLayout, nFaces)
	for fi := 0; fi < nFaces; fi++ {
		layouts[fi] = computeFaceLayout(model, fi)
	}

	// `maxAtlasDim` keeps each side comfortably below the WebGL2
	// MAX_TEXTURE_SIZE floor (browsers vary; 8192 is broadly safe).
	// On dense meshes with many large triangles the unconstrained
	// layout can blow past this; we then demote every face's tier
	// by one (clamped at 0) and re-layout. Halving every patch's
	// side cuts atlas pixel area by 4×, so a few iterations always
	// suffice in practice.
	const maxAtlasDim = 8192
	var slots []int
	var sides []int
	var bucketY []int
	var atlasW, atlasH, totalSamples int
	demotions := 0
	for {
		slots = make([]int, nFaces)
		counts := make([]int, nT*nT)
		totalSamples = 0
		for fi := 0; fi < nFaces; fi++ {
			b := bucketIdx(int(layouts[fi].WT), int(layouts[fi].HT))
			slots[fi] = counts[b]
			counts[b]++
			totalSamples += tiers[layouts[fi].WT] * tiers[layouts[fi].HT]
		}

		sides = make([]int, nT*nT)
		bucketH := make([]int, nT*nT)
		bucketY = make([]int, nT*nT)
		maxWidth := 0
		for b, count := range counts {
			if count == 0 {
				continue
			}
			wT := b / nT
			hT := b % nT
			W := tiers[wT]
			H := tiers[hT]
			side := int(math.Ceil(math.Sqrt(float64(count))))
			sides[b] = side
			w := side * W
			if w > maxWidth {
				maxWidth = w
			}
			bucketH[b] = int(math.Ceil(float64(count)/float64(side))) * H
		}
		totalH := 0
		for b := range counts {
			bucketY[b] = totalH
			totalH += bucketH[b]
		}
		if maxWidth == 0 || totalH == 0 {
			return nil, nil
		}
		atlasW = maxWidth
		atlasH = totalH
		if atlasW <= maxAtlasDim && atlasH <= maxAtlasDim {
			break
		}
		// Demote: drop every face by one tier (clamped at 0). If
		// nothing can demote, give up — the smallest tier is
		// already too dense for the cap (essentially impossible
		// since N=2 with sqrt(nFaces) packing yields atlasW ≈
		// 2*ceil(sqrt(nFaces)), well under 8192 even for millions
		// of faces).
		anyDemoted := false
		for i := range layouts {
			if layouts[i].WT > 0 {
				layouts[i].WT--
				anyDemoted = true
			}
			if layouts[i].HT > 0 {
				layouts[i].HT--
				anyDemoted = true
			}
		}
		if !anyDemoted {
			return nil, fmt.Errorf("MaterialX atlas %d×%d exceeds %dpx cap at smallest tier", atlasW, atlasH, maxAtlasDim)
		}
		demotions++
	}
	if demotions > 0 {
		plog.Printf("MaterialX atlas: demoted tier sizes %d× to fit %dpx cap (final %d×%d)", demotions, maxAtlasDim, atlasW, atlasH)
	}

	img := image.NewNRGBA(image.Rect(0, 0, atlasW, atlasH))
	uvs := make([]float32, nFaces*6)

	workers := runtime.NumCPU()
	if workers > nFaces {
		workers = nFaces
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	var done atomic.Int64
	reportEvery := totalSamples / 100
	if reportEvery < 1 {
		reportEvery = 1
	}

	atlasWf := float32(atlasW)
	atlasHf := float32(atlasH)

	for w := 0; w < workers; w++ {
		fLo := w * nFaces / workers
		fHi := (w + 1) * nFaces / workers
		if w == workers-1 {
			fHi = nFaces
		}
		wg.Add(1)
		go func(fLo, fHi int) {
			defer wg.Done()
			localBatch := 0
			for fi := fLo; fi < fHi; fi++ {
				// Cancellation: per-face check keeps the bake's abort
				// latency at one patch (≤ 64×64 samples).
				if ctx.Err() != nil {
					return
				}
				lay := layouts[fi]
				W := tiers[lay.WT]
				H := tiers[lay.HT]
				b := bucketIdx(int(lay.WT), int(lay.HT))
				slot := slots[fi]
				row := slot / sides[b]
				col := slot % sides[b]
				patchX := col * W
				patchY := bucketY[b] + row*H

				// 3D vectors for this face's longest-edge frame.
				f := model.Faces[fi]
				A := model.Vertices[f[lay.AIdx]]
				B := model.Vertices[f[lay.BIdx]]
				C := model.Vertices[f[lay.CIdx]]
				abx, aby, abz := B[0]-A[0], B[1]-A[1], B[2]-A[2]
				abLen := float32(math.Sqrt(float64(abx*abx + aby*aby + abz*abz)))
				abHatX := abx / abLen
				abHatY := aby / abLen
				abHatZ := abz / abLen
				acx, acy, acz := C[0]-A[0], C[1]-A[1], C[2]-A[2]
				sCsigned := acx*abHatX + acy*abHatY + acz*abHatZ
				perpX := acx - sCsigned*abHatX
				perpY := acy - sCsigned*abHatY
				perpZ := acz - sCsigned*abHatZ
				perpLen := lay.hC
				perpHatX := perpX / perpLen
				perpHatY := perpY / perpLen
				perpHatZ := perpZ / perpLen

				wReal := lay.sMax - lay.sMin
				hReal := lay.hC
				wDenom := float32(W - 1)
				hDenom := float32(H - 1)
				if wDenom <= 0 {
					wDenom = 1
				}
				if hDenom <= 0 {
					hDenom = 1
				}

				// Per-vertex UVs in atlas pixel coords. Each face
				// vertex maps to its (s, t) coord in the longest-edge
				// frame, then to a pixel within the patch. The
				// half-pixel inset places vertices at texel centers
				// — under nearest filtering this is harmless, and
				// preserves correctness if the frontend ever switches
				// back to linear filtering (which would otherwise
				// bleed across patch boundaries).
				sToPix := func(s float32) float32 {
					return float32(patchX) + 0.5 + (s-lay.sMin)/wReal*wDenom
				}
				tToPix := func(t float32) float32 {
					return float32(patchY) + 0.5 + t/hReal*hDenom
				}
				// A: (s=0, t=0), B: (s=abLen, t=0), C: (s=sCsigned, t=hC).
				uA := sToPix(0) / atlasWf
				vA := tToPix(0) / atlasHf
				uB := sToPix(abLen) / atlasWf
				vB := tToPix(0) / atlasHf
				uC := sToPix(sCsigned) / atlasWf
				vC := tToPix(lay.hC) / atlasHf
				// Write UVs in the original face-vertex order.
				uvs[fi*6+int(lay.AIdx)*2+0] = uA
				uvs[fi*6+int(lay.AIdx)*2+1] = vA
				uvs[fi*6+int(lay.BIdx)*2+0] = uB
				uvs[fi*6+int(lay.BIdx)*2+1] = vB
				uvs[fi*6+int(lay.CIdx)*2+0] = uC
				uvs[fi*6+int(lay.CIdx)*2+1] = vC

				patchSamples := W * H
				if model.NoTextureMask != nil && !model.NoTextureMask[fi] {
					localBatch += patchSamples
					continue
				}
				normal := voxel.FaceNormal(fi, model)

				for j := 0; j < H; j++ {
					t := float32(j) / hDenom * hReal
					for i := 0; i < W; i++ {
						s := lay.sMin + float32(i)/wDenom*wReal
						pos := [3]float32{
							A[0] + s*abHatX + t*perpHatX,
							A[1] + s*abHatY + t*perpHatY,
							A[2] + s*abHatZ + t*perpHatZ,
						}
						rgb := override.SampleBaseColor(voxel.BaseColorContext{
							Pos:    pos,
							Normal: normal,
						})
						img.SetNRGBA(patchX+i, patchY+j, color.NRGBA{rgb[0], rgb[1], rgb[2], 255})
					}
				}
				localBatch += patchSamples
				if localBatch >= reportEvery {
					cur := done.Add(int64(localBatch))
					if progressCB != nil {
						progressCB(int(cur))
					}
					localBatch = 0
				}
			}
			if localBatch > 0 {
				cur := done.Add(int64(localBatch))
				if progressCB != nil {
					progressCB(int(cur))
				}
			}
		}(fLo, fHi)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if progressCB != nil {
		progressCB(totalSamples)
	}

	encoded := encodeAtlasTexture(img)
	if encoded == "" {
		return nil, fmt.Errorf("encode MaterialX atlas: empty result")
	}
	return &BaseColorAtlas{
		Image:         encoded,
		Width:         int32(atlasW),
		Height:        int32(atlasH),
		FaceVertexUVs: uvs,
	}, nil
}

// signOrPos returns -1 when v < 0, +1 otherwise (including 0).
// Triplanar UV flipping needs a deterministic sign for the zero-normal
// case, where any choice is fine because the corresponding weight is
// zero (or 1/3 in the degenerate-normal fallback, where the flip
// doesn't visually matter either).
func signOrPos(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func rgbToBytes(rgb [3]float64) [3]uint8 {
	return [3]uint8{
		floatToByte(rgb[0]),
		floatToByte(rgb[1]),
		floatToByte(rgb[2]),
	}
}

// floatToByte quantizes a [0, 1] float to an 8-bit channel value.
//
// On the triplanar path, identical 8-bit sub-samples can come back at
// ±1 from the input byte: each sub-sample is rgb_float = byte/255,
// then weighted-sum across three planes accumulates a few ULPs of FP
// error before the round step here. Practically invisible against the
// dithering that runs downstream, but worth knowing if a future
// debugger asks "why isn't this pixel exactly equal to the texel".
//
// NaN propagates through arithmetic and fails both comparison
// branches below; uint8(NaN) is implementation-defined in Go. Pin it
// to 0 so a malformed graph evaluates to black instead of random
// per-voxel garbage.
func floatToByte(f float64) uint8 {
	if f != f {
		return 0
	}
	v := f*255 + 0.5
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

// NewMaterialXOverride parses the .mtlx (or .zip containing one) at
// path and returns a BaseColorOverride that triplanar-projects its
// default base-color graph. Unlike StageCache.baseColorOverride, this
// does not memoize — each call re-parses the package. Intended for
// one-off uses outside the pipeline (e.g. test fixture generators).
//
// tileMM scales world-space mm into the procedural's shading frame
// (values <= 0 are treated as 1 mm). triplanarSharpness only matters
// for image-backed graphs; <= 0 picks a sensible default of 4.
func NewMaterialXOverride(path string, tileMM, triplanarSharpness float64) (voxel.BaseColorOverride, error) {
	doc, err := materialx.ParsePackage(path)
	if err != nil {
		return nil, fmt.Errorf("MaterialX %q: %w", path, err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		return nil, fmt.Errorf("MaterialX %q: %w", path, err)
	}
	if s == nil {
		return nil, nil
	}
	if tileMM <= 0 {
		tileMM = 1
	}
	if triplanarSharpness <= 0 {
		triplanarSharpness = 4
	}
	return &materialxOverride{
		sampler:   s,
		invTileMM: 1 / tileMM,
		useUV:     s.UsesUV(),
		sharpness: triplanarSharpness,
	}, nil
}

// baseColorOverride wraps the cached materialx.Sampler for the package
// at path with a per-run tile/triplanar config. tileMM scales
// world-space mm into the procedural's shading frame (values <= 0 are
// treated as 1 mm). triplanarSharpness only matters for image-backed
// graphs; <= 0 picks a sensible default. Returns (nil, nil) when
// path is empty so callers can pass the result straight through to
// the voxelizer. The expensive parts (XML parse + image decode) are
// memoized on StageCache, so applyBaseColor and the voxelize stage
// share one parse per pipeline run.
//
// On parse error, tracker.Warn is invoked once per session per
// (path, mtime, size) — applyBaseColor and Voxelize both call this
// per run, and we don't want the same toast twice. The error is
// still returned so callers can skip downstream work, but only the
// first call surfaces it to the user.
func (c *StageCache) baseColorOverride(path string, tileMM, triplanarSharpness float64, tracker progress.Tracker) (voxel.BaseColorOverride, error) {
	if path == "" {
		c.mtlxWarnedPath = ""
		return nil, nil
	}
	s, err := c.materialXSampler(path)
	if err != nil {
		err = fmt.Errorf("MaterialX %q: %w", path, err)
		if c.mtlxWarnedPath != path {
			tracker.Warn(progress.WarnKindMaterialXBaseColor, fmt.Sprintf("ignoring MaterialX base color: %v", err))
			c.mtlxWarnedPath = path
		}
		return nil, err
	}
	// Successful resolution clears the dedup so a future failure on
	// this path warns again.
	c.mtlxWarnedPath = ""
	if s == nil {
		return nil, nil
	}
	if tileMM <= 0 {
		tileMM = 1
	}
	return &materialxOverride{
		sampler:   s,
		invTileMM: 1 / tileMM,
		useUV:     s.UsesUV(),
		sharpness: triplanarSharpness,
	}, nil
}
