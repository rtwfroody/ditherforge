package cellslicer

import (
	"math"
	"testing"
)

// squareFootprint builds a single CCW square coverTarget [0,size]².
func squareFootprint(size float32) *Footprint {
	lp := Loop{Points: []Point2{{0, 0}, {size, 0}, {size, size}, {0, size}}}
	lp.RefreshDerived()
	return ComputeFootprint([]Loop{lp}, nil)
}

// TestColorRegionsCheckerboard: a checkerboard with squares larger than
// a cell segments into many distinct monochrome regions, and those
// regions still tile the whole coverTarget.
func TestColorRegionsCheckerboard(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0
	const checker = 2.0 // ≥ cellSize, so every square is honourable

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		cx := int(math.Floor(float64(x / checker)))
		cy := int(math.Floor(float64(y / checker)))
		if (cx+cy)%2 == 0 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) < 4 {
		t.Fatalf("expected many checker regions, got %d", len(regions))
	}

	// Regions tile coverTarget: total area ≈ cover area.
	var total float64
	for _, r := range regions {
		total += footprintArea(r)
	}
	want := footprintArea(cover)
	if d := math.Abs(total-want) / want; d > 0.05 {
		t.Fatalf("regions do not tile coverTarget: total=%.3f want=%.3f (%.1f%% off)", total, want, d*100)
	}
}

// TestColorRegionsGradientNotCut: a smooth black→white gradient has no
// sharp edge, so above a modest ΔE threshold it must stay ONE region
// (ColorRegions returns nil) rather than over-segmenting into bands.
func TestColorRegionsGradientNotCut(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	// Linear ramp across x: adjacent grid nodes differ by a tiny ΔE.
	sample := func(x, y float32) ([3]uint8, bool) {
		v := uint8(x / size * 255)
		return [3]uint8{v, v, v}, true
	}

	if regions := ColorRegions(cover, cellSize, 20, sample); regions != nil {
		t.Fatalf("smooth gradient should not be cut at ΔE=20, got %d regions", len(regions))
	}
	// A low threshold WILL start cutting the ramp into bands.
	if regions := ColorRegions(cover, cellSize, 1, sample); len(regions) < 2 {
		t.Fatalf("at ΔE=1 the ramp should over-segment, got %d regions", len(regions))
	}
}

// TestColorRegionsIsolatedIslandCovered: a small disconnected coverTarget
// island of a distinct colour must be covered by some region, never left
// in no region — an uncovered island is a hole in the printed shell. (An
// isolated island is "deep" by isDeep's definition, so it survives via the
// keep path; the enforceMinSize freeze path is the additional safety net
// for the narrower non-deep, no-mergeable-neighbour case.) Locks the
// disjoint-union==coverTarget invariant against regressions in either path.
func TestColorRegionsIsolatedIslandCovered(t *testing.T) {
	const cellSize = 1.0

	// Two disconnected components: a fat 6×6 square at the origin and a
	// tiny 0.5mm island far away. Different colours, so the grid segments
	// them; the island is sub-cell with no neighbour.
	big := Loop{Points: []Point2{{0, 0}, {6, 0}, {6, 6}, {0, 6}}}
	big.RefreshDerived()
	speck := Loop{Points: []Point2{{20, 20}, {20.5, 20}, {20.5, 20.5}, {20, 20.5}}}
	speck.RefreshDerived()
	cover := ComputeFootprint([]Loop{big, speck}, nil)

	sample := func(x, y float32) ([3]uint8, bool) {
		if x > 10 {
			return [3]uint8{0, 0, 0}, true // the speck
		}
		return [3]uint8{255, 255, 255}, true // the big square
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) < 2 {
		t.Fatalf("expected the island kept as its own region, got %d regions", len(regions))
	}
	// The island's location must be covered by some region — the bug
	// dropped the island, leaving (20.25,20.25) in no region at all.
	covered := false
	for _, r := range regions {
		if r.Contains(20.25, 20.25) {
			covered = true
			break
		}
	}
	if !covered {
		t.Fatalf("isolated sub-cell island was dropped — its location is in no region (hole in shell)")
	}
}

// TestColorRegionsSubCellSpeckMerged: a colour feature smaller than a
// cell must NOT become its own region — it is merged into its
// neighbour, leaving a single colour and thus no cut (nil result).
func TestColorRegionsSubCellSpeckMerged(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	// A 0.4mm black speck (< cellSize) centred at (4,4) on white.
	sample := func(x, y float32) ([3]uint8, bool) {
		if x >= 3.8 && x <= 4.2 && y >= 3.8 && y <= 4.2 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if regions != nil {
		t.Fatalf("sub-cell speck should merge away (nil regions), got %d regions", len(regions))
	}
}

// TestColorRegionsHalfSplit: a single high-contrast edge between two
// large regions is honoured — exactly two regions, splitting cover in
// half along the colour boundary.
func TestColorRegionsHalfSplit(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		if x < 4.0 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions across one edge, got %d", len(regions))
	}
	// Each half ≈ 32mm².
	for i, r := range regions {
		a := footprintArea(r)
		if a < 24 || a > 40 {
			t.Errorf("region %d area %.2f not ≈ half of 64", i, a)
		}
	}
}

// TestColorRegionsReachSilhouette pins the silhouette-coverage fix.
// Region footprints are built from a grid of pitch×pitch squares; on an
// edge that does not land on a grid node (here cover spans [0,size] with
// size NOT a multiple of the pitch), the outermost in-region node sits
// below the edge and the bare node±half squares fall up to ~half a pitch
// short of the max-X / max-Y silhouette. On a vertical wall that leaves a
// boundary cell ~10µm inside the surface, the 5µm open-edge clip bloat
// can't bridge it, and the per-cell clip prism misses the wall → the
// whole max-X / max-Y wall comes out as holes. The region footprints must
// therefore reach the cover silhouette on every side, not just the
// grid-aligned ones.
func TestColorRegionsReachSilhouette(t *testing.T) {
	const cellSize = 1.0
	// size/pitch is non-integer (pitch = cellSize/4 = 0.25; 8.2/0.25 =
	// 32.8), so the max edge falls between nodes — the failing case.
	const size = 8.2

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		if x < size/2 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions across one edge, got %d", len(regions))
	}

	// Union extent of all region footprints.
	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, r := range regions {
		for _, lp := range r.Loops {
			for _, p := range lp.Points {
				minX = float32(math.Min(float64(minX), float64(p[0])))
				minY = float32(math.Min(float64(minY), float64(p[1])))
				maxX = float32(math.Max(float64(maxX), float64(p[0])))
				maxY = float32(math.Max(float64(maxY), float64(p[1])))
			}
		}
	}
	// Tolerance well under the ~half-pitch (0.125mm) shortfall the bug
	// produced, but above Clipper's integer-grid rounding.
	const tol = 0.02
	if minX > tol || minY > tol {
		t.Errorf("regions do not reach the min silhouette: min=(%.4f,%.4f) want ≤(%.2f,%.2f)", minX, minY, tol, tol)
	}
	if maxX < size-tol || maxY < size-tol {
		t.Errorf("regions fall short of the max silhouette: max=(%.4f,%.4f) want ≥(%.4f,%.4f)", maxX, maxY, size-tol, size-tol)
	}
	// The max-X/max-Y CORNER must be covered too. Axis-only extension
	// reaches each edge with SOME point but leaves the outer diagonal
	// quadrant — and hence the corner itself — uncovered; the convex-corner
	// diagonal rect fixes that.
	corner := float32(size - tol)
	covered := false
	for _, r := range regions {
		if r.Contains(corner, corner) {
			covered = true
			break
		}
	}
	if !covered {
		t.Errorf("max corner (%.3f,%.3f) is in no region — convex corner left a hole", corner, corner)
	}
}

// TestColorRegionsNoNeckOverlap pins the boundary extension against the
// disjoint-regions invariant. A printable OUTSIDE slot in coverTarget that
// lies on a colour cut must NOT let the two regions' silhouette extensions
// poke across the slot into each other. With the original full-pitch
// extension the cells on each side reached past the slot into the opposite
// region (~0.3mm² overlap → doubled cells); the half-pitch extension reaches
// only as far as the slot, where the cover intersect clips it, so any slot
// at least cellSize/2 wide stays cleanly disjoint. (Sub-cellSize/2 slots —
// below the cellSize/4 grid's resolution and the nozzle's printable gap —
// retain a bounded ~(cellSize/8)² corner sliver, in the same noise class as
// Clipper's coincident-edge tie-breaks.)
func TestColorRegionsNoNeckOverlap(t *testing.T) {
	const cellSize = 1.0 // pitch = 0.25, so a ≥0.5mm slot is fully resolved
	// Square [0,10]² with a 0.6mm-wide slot cut into the top edge down to
	// y=5, straddling the x=5 colour cut — a printable gap the grid resolves.
	outer := Loop{Points: []Point2{
		{0, 0}, {10, 0}, {10, 10}, {5.3, 10}, {5.3, 5}, {4.7, 5}, {4.7, 10}, {0, 10},
	}}
	outer.RefreshDerived()
	cover := ComputeFootprint([]Loop{outer}, nil)

	sample := func(x, y float32) ([3]uint8, bool) {
		if x < 5.0 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions across the colour cut, got %d", len(regions))
	}
	// The two regions must stay disjoint across the slot.
	const tol = 0.001
	inter := FootprintIntersect(regions[0], regions[1])
	if inter != nil {
		if a := footprintArea(inter); a > tol {
			t.Errorf("regions overlap across the slot by %.4f mm² (want ≤ %.3f) — extension bridged the neck", a, tol)
		}
	}
}

// TestColorRegionsTendrilCeded pins the shallow-node reassignment
// (reassignShallowNodes). A black half-plane with a 0.3mm black strip
// (< cellSize) running along the bottom silhouette under the white half
// is ONE flood-fill component, and it is deep (the half-plane admits
// plenty of cellSize disks), so enforceMinSize's whole-region merge never
// touches it — the strip used to survive as a tendril of the black region
// and tile into sub-cellSize sliver cells via ringSeeds' thin-feature
// fallback. The tendril's shallow nodes must instead be ceded to the
// white region, whose footprint then reaches the bottom silhouette there.
func TestColorRegionsTendrilCeded(t *testing.T) {
	const size = 10.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		if x < 5.0 || y < 0.3 {
			return [3]uint8{0, 0, 0}, true // black half + bottom strip
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions, got %d", len(regions))
	}
	regionAt := func(x, y float32) *Footprint {
		for _, r := range regions {
			if r.Contains(x, y) {
				return r
			}
		}
		return nil
	}
	black := regionAt(2, 5)
	white := regionAt(7.5, 5)
	if black == nil || white == nil || black == white {
		t.Fatalf("could not identify distinct black/white regions")
	}
	// Mid-tendril, well past the sub-cell stub allowed near the
	// attachment point: must belong to WHITE now.
	if black.Contains(7.5, 0.1) {
		t.Errorf("tendril at (7.5, 0.1) still belongs to the black region — shallow nodes not ceded")
	}
	if !white.Contains(7.5, 0.1) {
		t.Errorf("tendril at (7.5, 0.1) is not covered by the white region — hole in coverTarget")
	}
	// The regions must still tile the whole coverTarget.
	var total float64
	for _, r := range regions {
		total += footprintArea(r)
	}
	want := footprintArea(cover)
	if d := math.Abs(total-want) / want; d > 0.02 {
		t.Errorf("regions do not tile coverTarget after reassignment: total=%.3f want=%.3f (%.1f%% off)", total, want, d*100)
	}
}

// gridFromLabels builds a colorGrid directly from a rows×cols label map and
// a per-label colour, for white-box tests of the merge logic. label -1 means
// "outside" (inside=false). All labelled nodes are surface hits (never miss).
func gridFromLabels(labels [][]int32, colors map[int32][3]uint8) *colorGrid {
	rows := len(labels)
	cols := len(labels[0])
	g := &colorGrid{
		pitch:  0.25,
		cols:   cols,
		rows:   rows,
		inside: make([]bool, cols*rows),
		col:    make([][3]uint8, cols*rows),
		miss:   make([]bool, cols*rows),
		label:  make([]int32, cols*rows),
	}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			lab := labels[r][c]
			g.label[idx] = lab
			if lab < 0 {
				continue
			}
			g.inside[idx] = true
			g.insideCount++
			g.col[idx] = colors[lab]
		}
	}
	return g
}

// TestMergeTargetPrefersClosestColor pins the ΔE-aware merge target choice.
// A thin dark strip (label 1) sits between a white region (label 0) and a
// black region (label 2). The strip touches WHITE on more edges than black,
// so the old adjacency-only rule merged it into white — leaving the surviving
// cut at the low-contrast dark↔black edge and letting white cells average
// toward grey. Perceptually the strip is far closer to black, so the fix
// merges it into black, keeping the crisp white↔dark cut.
//
// It exercises mergeTarget directly — the helper enforceMinSize actually uses
// for target selection — rather than a victim-selection wrapper, so there is
// no test-only code path that could silently diverge from production.
func TestMergeTargetPrefersClosestColor(t *testing.T) {
	const white, strip, black = int32(0), int32(1), int32(2)
	// 3×4 grid. Strip = the two nodes in column 1, rows 0-1 (2 nodes, the
	// smallest region → the victim). White wraps under it at (2,1), so the
	// strip shares 3 edges with white vs 2 with black — white wins on
	// adjacency, black wins on colour.
	labels := [][]int32{
		{white, strip, black, black},
		{white, strip, black, black},
		{white, white, black, black},
	}
	colors := map[int32][3]uint8{
		white: {255, 255, 255},
		strip: {40, 40, 40}, // dark grey: ΔE-close to black, far from white
		black: {0, 0, 0},
	}
	g := gridFromLabels(labels, colors)
	s := g.newMergeState()

	// The strip is the smallest region, so enforceMinSize's heap would pop it
	// as the victim first.
	if s.area[strip] != 2 {
		t.Fatalf("strip area = %d, want 2", s.area[strip])
	}
	for lab, a := range s.area {
		if a < s.area[strip] {
			t.Fatalf("label %d has area %d < strip's %d; strip must be the smallest victim", lab, a, s.area[strip])
		}
	}

	if target := g.mergeTarget(strip, s); target != black {
		t.Fatalf("merge target = %d, want the perceptually closest neighbour (black=%d); "+
			"adjacency-only would have picked white=%d", target, black, white)
	}

	// Sanity: the strip really does touch white more than black, so this
	// test would pass trivially under the old rule only by coincidence.
	if dW, dB := deltaE76(colors[strip], colors[white]), deltaE76(colors[strip], colors[black]); dB >= dW {
		t.Fatalf("test setup broken: strip must be closer to black (ΔE_black=%.1f) than white (ΔE_white=%.1f)", dB, dW)
	}
}
