package split

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// cutBuilder accumulates the two output halves while iterating over the
// input mesh. Vertex indices in each half are independent: a vertex on
// the original mesh that ends up in both halves (i.e. lying exactly on
// the plane) is given two distinct new indices, one per half.
//
// Cut-polygon midpoint vertices are similarly per-half: half 0 and half
// 1 each carry their own copy of the midpoint at the same world
// position, so each half is a self-contained *LoadedModel.
type cutBuilder struct {
	src    *loader.LoadedModel
	plane  Plane
	halves [2]*loader.LoadedModel

	// vertMap[half][srcIdx] → new index in halves[half], or -1 if not
	// yet copied. We allocate two flat slices for speed since vertex
	// counts are known.
	vertMap [2][]int32

	// midMap[half][edgeKey] → new index in halves[half] for the
	// midpoint introduced when an input edge is cut by the plane.
	// Shared between the two triangles incident to the cut edge so each
	// half only gets one midpoint vertex per cut edge.
	midMap [2]map[edgeKey]uint32

	// cutEdges[half] is the list of directed edges (a, b) in halves[half]
	// vertex space that lie along the cut polygon. Direction is chosen
	// so the cap of half 0 is wound CCW when viewed from the +plane.Normal
	// side; half 1's edges are reversed so its cap is CCW when viewed
	// from -plane.Normal.
	cutEdges [2][][2]uint32

	// capFaces[half] receives the indices (in halves[half].Faces) of the
	// triangles emitted by triangulateCaps for half's cap.
	capFaces [2][]uint32
}

// edgeKey identifies an undirected edge between two source-mesh vertices.
type edgeKey struct {
	a, b uint32 // a < b
}

func makeEdgeKey(a, b uint32) edgeKey {
	if a < b {
		return edgeKey{a, b}
	}
	return edgeKey{b, a}
}

// newCutBuilder initialises a cutBuilder. halves[i] is allocated with
// space hints proportional to the source mesh; parallel arrays are
// allocated only when the source has them, to preserve nil semantics.
func newCutBuilder(src *loader.LoadedModel, plane Plane) *cutBuilder {
	b := &cutBuilder{
		src:   src,
		plane: plane,
	}
	for h := 0; h < 2; h++ {
		b.halves[h] = newEmptyHalf(src)
		b.vertMap[h] = make([]int32, len(src.Vertices))
		for i := range b.vertMap[h] {
			b.vertMap[h][i] = -1
		}
		b.midMap[h] = make(map[edgeKey]uint32)
	}
	return b
}

// newEmptyHalf returns a *LoadedModel whose parallel arrays mirror src's
// nil-or-non-nil pattern. Textures are shared by reference (immutable
// after load), and NumMeshes/Textures fields are copied verbatim.
func newEmptyHalf(src *loader.LoadedModel) *loader.LoadedModel {
	h := &loader.LoadedModel{
		Textures:  src.Textures,
		NumMeshes: src.NumMeshes,
	}
	if src.UVs != nil {
		h.UVs = make([][2]float32, 0)
	}
	if src.VertexColors != nil {
		h.VertexColors = make([][4]uint8, 0)
	}
	if src.FaceTextureIdx != nil {
		h.FaceTextureIdx = make([]int32, 0)
	}
	if src.FaceAlpha != nil {
		h.FaceAlpha = make([]float32, 0)
	}
	if src.FaceBaseColor != nil {
		h.FaceBaseColor = make([][4]uint8, 0)
	}
	if src.NoTextureMask != nil {
		h.NoTextureMask = make([]bool, 0)
	}
	if src.FaceMeshIdx != nil {
		h.FaceMeshIdx = make([]int32, 0)
	}
	return h
}

// processFaces iterates over every input face and emits the appropriate
// surface geometry into halves[0] and/or halves[1]. Cut-polygon edges
// are recorded in b.cutEdges for later loop recovery.
func (b *cutBuilder) processFaces(side []int8) error {
	for fi, f := range b.src.Faces {
		s0, s1, s2 := side[f[0]], side[f[1]], side[f[2]]
		switch {
		case s0 >= 0 && s1 >= 0 && s2 >= 0 && (s0+s1+s2) > 0:
			// Entirely on positive side (with possibly some on-plane vertices).
			b.emitWholeFace(1, fi, f, side)
		case s0 <= 0 && s1 <= 0 && s2 <= 0 && (s0+s1+s2) < 0:
			// Entirely on negative side.
			b.emitWholeFace(0, fi, f, side)
		case s0 == 0 && s1 == 0 && s2 == 0:
			// Triangle lies on the plane. Skip; on a watertight mesh
			// this is normally accompanied by topology that closes up
			// without it, but if it occurs we drop it rather than
			// guess a side.
			continue
		default:
			// Mixed sides: at least one strictly positive and one
			// strictly negative vertex. Split into 1 + 2 triangles.
			if err := b.splitFace(fi, f, side); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitWholeFace copies face f (already classified as belonging to half
// `h`) into halves[h], remapping vertex indices via copyVertex.
//
// If f has an edge with both endpoints lying exactly on the plane, that
// edge contributes to the cut polygon for the *other* half. The cut
// edge is recorded in the natural winding of the face on side h (i→j),
// reversed for the other half because the other-side surface walks the
// edge in the opposite direction.
func (b *cutBuilder) emitWholeFace(h int, fi int, f [3]uint32, side []int8) {
	var newF [3]uint32
	for i, vi := range f {
		newF[i] = b.copyVertex(h, vi)
	}
	b.appendFace(h, fi, newF)

	other := 1 - h
	for i := 0; i < 3; i++ {
		j := (i + 1) % 3
		if side[f[i]] == 0 && side[f[j]] == 0 {
			aOther := b.copyVertex(other, f[j])
			cOther := b.copyVertex(other, f[i])
			b.cutEdges[other] = append(b.cutEdges[other], [2]uint32{aOther, cOther})
		}
	}
}

// splitFace handles the mixed-side case: at least one + and at least
// one - vertex. Up to two midpoint vertices are introduced (one per cut
// edge) and the face is partitioned into:
//   - a single triangle on one side (the "isolated" vertex's side);
//   - one or two triangles on the other side, depending on whether the
//     third vertex lies on the plane.
func (b *cutBuilder) splitFace(fi int, f [3]uint32, side []int8) error {
	// Rotate the triangle so the isolated vertex (single + or single -)
	// is at index 0. This collapses the case analysis.
	//
	// Possible side patterns (with at least one + and at least one -):
	//   1+ / 2- :  + - -, - + -, - - +    → isolated = the +
	//   2+ / 1- :  - + +, + - +, + + -    → isolated = the -
	//   1+ / 1- / 1 zero
	//
	// We pick the rotation by counting +'s and -'s.
	var pluses, minuses int
	for _, vi := range f {
		switch side[vi] {
		case +1:
			pluses++
		case -1:
			minuses++
		}
	}

	// rotIdx is the rotation amount k so that f[(i+k)%3] places the
	// isolated vertex at slot 0. Cut.go's caller has already
	// rejected on-plane vertices, so for any mixed-side face exactly
	// one of {pluses, minuses} is 1 (the isolated side).
	var isolatedSide int8
	switch {
	case pluses == 1:
		isolatedSide = +1
	case minuses == 1:
		isolatedSide = -1
	default:
		return fmt.Errorf("split.Cut: unexpected mixed-face side pattern (%d, %d) on face %d", pluses, minuses, fi)
	}
	rotIdx := -1
	for i := 0; i < 3; i++ {
		if side[f[i]] == isolatedSide {
			rotIdx = i
			break
		}
	}
	if rotIdx < 0 {
		return fmt.Errorf("split.Cut: could not find isolated vertex on face %d", fi)
	}
	a := f[rotIdx]
	bV := f[(rotIdx+1)%3]
	c := f[(rotIdx+2)%3]
	sb, sc := side[bV], side[c]

	// The isolated vertex `a` is on side `sa`. The plane crosses edges
	// a-b and a-c (when sb and sc are both opposite-sign or zero).
	//
	// Subcase 1: sb and sc are both strictly opposite sign.
	//   Two midpoints: m_ab on edge a-b, m_ac on edge a-c.
	//   Isolated side gets:    a, m_ab, m_ac
	//   Other side gets:       b, c, m_ac  and  b, m_ac, ... wait
	// Let me redo: with `a` isolated, b and c are on the other side,
	// the other side is a quad (b, c, m_ac, m_ab) which we split into
	// (b, c, m_ac) and (b, m_ac, m_ab).
	//
	// Subcase 2: sb == 0, sc opposite of sa.
	//   One midpoint: m_ac. Edge a-b lies on the plane only at b.
	//   Isolated side gets:    a, b, m_ac
	//   Other side gets:       b, c, m_ac
	//   (Single cut edge (b, m_ac) along the plane.)
	//
	// Subcase 3: sc == 0, sb opposite of sa.
	//   Symmetric to 2.
	//
	// Subcase 4: sb == 0 and sc == 0 — impossible since `a` would not be
	// the unique isolated vertex; pluses or minuses would be 0.

	isoH := 0
	if isolatedSide == +1 {
		isoH = 1
	}
	otherH := 1 - isoH

	switch {
	case sb != 0 && sc != 0:
		// Subcase 1: full split.
		mAB_iso := b.midpointVertex(isoH, a, bV)
		mAB_oth := b.midpointVertex(otherH, a, bV)
		mAC_iso := b.midpointVertex(isoH, a, c)
		mAC_oth := b.midpointVertex(otherH, a, c)

		// Isolated side: triangle (a, m_ab, m_ac), winding preserved
		// from the original (a, b, c).
		aIso := b.copyVertex(isoH, a)
		b.appendFace(isoH, fi, [3]uint32{aIso, mAB_iso, mAC_iso})

		// Other side: quad (m_ab, b, c, m_ac) split into
		// (m_ab, b, c) and (m_ab, c, m_ac).
		bOth := b.copyVertex(otherH, bV)
		cOth := b.copyVertex(otherH, c)
		b.appendFace(otherH, fi, [3]uint32{mAB_oth, bOth, cOth})
		b.appendFace(otherH, fi, [3]uint32{mAB_oth, cOth, mAC_oth})

		// Cut edge follows each half's natural triangle winding. On
		// the isolated triangle (a, m_ab, m_ac) the cut edge is
		// m_ab → m_ac. On the other side's diagonal triangle
		// (m_ab, c, m_ac) the cut edge is m_ac → m_ab — the reverse,
		// as expected of two faces sharing the same cut polygon.
		b.cutEdges[isoH] = append(b.cutEdges[isoH], [2]uint32{mAB_iso, mAC_iso})
		b.cutEdges[otherH] = append(b.cutEdges[otherH], [2]uint32{mAC_oth, mAB_oth})

	case sb == 0 && sc != 0:
		// Subcase 2: edge a-b ends on the plane at b; only a-c is cut.
		mAC_iso := b.midpointVertex(isoH, a, c)
		mAC_oth := b.midpointVertex(otherH, a, c)
		aIso := b.copyVertex(isoH, a)
		bIso := b.copyVertex(isoH, bV)
		b.appendFace(isoH, fi, [3]uint32{aIso, bIso, mAC_iso})

		bOth := b.copyVertex(otherH, bV)
		cOth := b.copyVertex(otherH, c)
		b.appendFace(otherH, fi, [3]uint32{bOth, cOth, mAC_oth})

		// Cut edge connects b (on plane) and m_ac. Natural winding in
		// (a, b, m_ac) is b → m_ac; in (b, c, m_ac) it's m_ac → b.
		b.cutEdges[isoH] = append(b.cutEdges[isoH], [2]uint32{bIso, mAC_iso})
		b.cutEdges[otherH] = append(b.cutEdges[otherH], [2]uint32{mAC_oth, bOth})

	case sc == 0 && sb != 0:
		// Subcase 3: edge a-c ends on the plane at c; only a-b is cut.
		mAB_iso := b.midpointVertex(isoH, a, bV)
		mAB_oth := b.midpointVertex(otherH, a, bV)
		aIso := b.copyVertex(isoH, a)
		cIso := b.copyVertex(isoH, c)
		b.appendFace(isoH, fi, [3]uint32{aIso, mAB_iso, cIso})

		bOth := b.copyVertex(otherH, bV)
		cOth := b.copyVertex(otherH, c)
		b.appendFace(otherH, fi, [3]uint32{mAB_oth, bOth, cOth})

		// Cut edge connects m_ab and c. Natural winding in
		// (a, m_ab, c) is m_ab → c; in (m_ab, b, c) it's c → m_ab.
		b.cutEdges[isoH] = append(b.cutEdges[isoH], [2]uint32{mAB_iso, cIso})
		b.cutEdges[otherH] = append(b.cutEdges[otherH], [2]uint32{cOth, mAB_oth})

	default:
		return fmt.Errorf("split.Cut: unexpected on-plane edge configuration on face %d", fi)
	}
	return nil
}

// copyVertex returns the index in halves[h] of the source vertex `srcIdx`,
// allocating a new entry on first use. Parallel arrays in halves[h] are
// kept in sync by appending the corresponding source value.
func (b *cutBuilder) copyVertex(h int, srcIdx uint32) uint32 {
	if existing := b.vertMap[h][srcIdx]; existing >= 0 {
		return uint32(existing)
	}
	half := b.halves[h]
	newIdx := uint32(len(half.Vertices))
	half.Vertices = append(half.Vertices, b.src.Vertices[srcIdx])
	if half.UVs != nil {
		half.UVs = append(half.UVs, b.src.UVs[srcIdx])
	}
	if half.VertexColors != nil {
		half.VertexColors = append(half.VertexColors, b.src.VertexColors[srcIdx])
	}
	b.vertMap[h][srcIdx] = int32(newIdx)
	return newIdx
}

// midpointVertex returns the index in halves[h] of the midpoint vertex
// on the cut edge (srcA, srcB), creating it on first encounter and
// caching by undirected edge key. The midpoint's parameter t along the
// edge is determined by the two endpoints' signed distances to the
// plane, so it lies exactly on the plane (modulo float precision).
func (b *cutBuilder) midpointVertex(h int, srcA, srcB uint32) uint32 {
	key := makeEdgeKey(srcA, srcB)
	if existing, ok := b.midMap[h][key]; ok {
		return existing
	}
	pa := b.src.Vertices[srcA]
	pb := b.src.Vertices[srcB]
	da := b.plane.signedDistance(pa)
	db := b.plane.signedDistance(pb)
	t := da / (da - db) // valid because da, db have opposite signs
	v := [3]float32{
		pa[0] + float32(t)*(pb[0]-pa[0]),
		pa[1] + float32(t)*(pb[1]-pa[1]),
		pa[2] + float32(t)*(pb[2]-pa[2]),
	}
	half := b.halves[h]
	newIdx := uint32(len(half.Vertices))
	half.Vertices = append(half.Vertices, v)
	if half.UVs != nil {
		ua := b.src.UVs[srcA]
		ub := b.src.UVs[srcB]
		half.UVs = append(half.UVs, [2]float32{
			ua[0] + float32(t)*(ub[0]-ua[0]),
			ua[1] + float32(t)*(ub[1]-ua[1]),
		})
	}
	if half.VertexColors != nil {
		ca := b.src.VertexColors[srcA]
		cb := b.src.VertexColors[srcB]
		half.VertexColors = append(half.VertexColors, [4]uint8{
			lerpU8(ca[0], cb[0], t),
			lerpU8(ca[1], cb[1], t),
			lerpU8(ca[2], cb[2], t),
			lerpU8(ca[3], cb[3], t),
		})
	}
	b.midMap[h][key] = newIdx
	return newIdx
}

// appendFace adds a face to halves[h], copying per-face attributes from
// the source face (or default cap-style values if srcFace == -1, used
// during cap triangulation in triangulateCaps).
func (b *cutBuilder) appendFace(h int, srcFace int, f [3]uint32) {
	half := b.halves[h]
	half.Faces = append(half.Faces, f)
	if half.FaceTextureIdx != nil {
		if srcFace >= 0 {
			half.FaceTextureIdx = append(half.FaceTextureIdx, b.src.FaceTextureIdx[srcFace])
		} else {
			// Cap face: sentinel "no texture".
			half.FaceTextureIdx = append(half.FaceTextureIdx, int32(len(b.src.Textures)))
		}
	}
	if half.FaceAlpha != nil {
		if srcFace >= 0 {
			half.FaceAlpha = append(half.FaceAlpha, b.src.FaceAlpha[srcFace])
		} else {
			half.FaceAlpha = append(half.FaceAlpha, 1)
		}
	}
	if half.FaceBaseColor != nil {
		if srcFace >= 0 {
			half.FaceBaseColor = append(half.FaceBaseColor, b.src.FaceBaseColor[srcFace])
		} else {
			half.FaceBaseColor = append(half.FaceBaseColor, [4]uint8{128, 128, 128, 255})
		}
	}
	if half.NoTextureMask != nil {
		if srcFace >= 0 {
			half.NoTextureMask = append(half.NoTextureMask, b.src.NoTextureMask[srcFace])
		} else {
			half.NoTextureMask = append(half.NoTextureMask, true)
		}
	}
	if half.FaceMeshIdx != nil {
		if srcFace >= 0 {
			half.FaceMeshIdx = append(half.FaceMeshIdx, b.src.FaceMeshIdx[srcFace])
		} else {
			half.FaceMeshIdx = append(half.FaceMeshIdx, 0)
		}
	}
}

// lerpU8 linearly interpolates between a and b at parameter t∈[0,1].
func lerpU8(a, b uint8, t float64) uint8 {
	x := float64(a) + t*(float64(b)-float64(a))
	if x < 0 {
		x = 0
	} else if x > 255 {
		x = 255
	}
	return uint8(x + 0.5)
}
