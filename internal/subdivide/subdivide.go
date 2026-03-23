// Package subdivide implements adaptive mesh subdivision using longest-edge
// bisection: only the longest edge of each too-long face is split per
// iteration, avoiding unnecessary refinement of short edges.
//
// Subdivision returns a tree of Nodes (one root per original face). Leaf
// nodes are the final subdivided faces; internal nodes are faces that were
// split. After color assignment on the leaves, Merge collapses any subtree
// whose leaves all share the same color back into its root face, reducing
// output triangle count.
package subdivide

import (
	"fmt"
	"math"

	"github.com/rtwfroody/text2filament/internal/loader"
)

const MaxIter = 32

// TooManyVerticesError is returned when the estimated vertex count would
// exceed the caller's budget.
type TooManyVerticesError struct {
	Estimated int
}

func (e *TooManyVerticesError) Error() string {
	return fmt.Sprintf("estimated %d vertices would exceed budget", e.Estimated)
}

// Node represents one face at some level of the subdivision tree.
// If Children is nil, this is a leaf (a final subdivided face).
// If Children is non-nil, this face was split; its children tile the same area.
type Node struct {
	Face     [3]uint32
	TexIdx   int32
	Children []*Node
}

// FaceColor pairs a face with its assigned palette color index, for use after
// Merge collapses uniform subtrees.
type FaceColor struct {
	Face   [3]uint32
	TexIdx int32
	Color  int32
}

func makeEdge(a, b uint32) [2]uint32 {
	if a < b {
		return [2]uint32{a, b}
	}
	return [2]uint32{b, a}
}

func edgeLen(verts [][3]float32, a, b uint32) float32 {
	dx := verts[a][0] - verts[b][0]
	dy := verts[a][1] - verts[b][1]
	dz := verts[a][2] - verts[b][2]
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// Subdivide adaptively subdivides the mesh until no edge exceeds maxEdgeMM,
// using longest-edge bisection. Returns a tree of Nodes (one root per original
// face), plus the shared vertex and UV arrays. The tree is used by Leaves (to
// build a flat model for color sampling) and Merge (to collapse uniform
// subtrees after color assignment).
func Subdivide(model *loader.LoadedModel, maxEdgeMM float32, maxVertices int) (
	roots []*Node,
	verts [][3]float32,
	uvs [][2]float32,
	err error,
) {
	verts = append([][3]float32(nil), model.Vertices...)
	uvs = append([][2]float32(nil), model.UVs...)

	// Create one root node per original face.
	roots = make([]*Node, len(model.Faces))
	for i, f := range model.Faces {
		roots[i] = &Node{Face: f, TexIdx: model.FaceTextureIdx[i]}
	}

	// Working set: current nodes and their corresponding face/tex data.
	// Initially the entire mesh; shrinks as faces become "done".
	currentNodes := make([]*Node, len(roots))
	copy(currentNodes, roots)
	faces := append([][3]uint32(nil), model.Faces...)
	faceTex := append([]int32(nil), model.FaceTextureIdx...)

	for iter := 0; iter <= MaxIter; iter++ {
		// Partition faces into ok (all edges short) and too-long.
		var longNodes []*Node
		var longFaces [][3]uint32
		var longFaceTex []int32

		for fi, face := range faces {
			tooLong := edgeLen(verts, face[0], face[1]) > maxEdgeMM ||
				edgeLen(verts, face[1], face[2]) > maxEdgeMM ||
				edgeLen(verts, face[2], face[0]) > maxEdgeMM
			if tooLong {
				longNodes = append(longNodes, currentNodes[fi])
				longFaces = append(longFaces, face)
				longFaceTex = append(longFaceTex, faceTex[fi])
			}
			// ok faces: their nodes are already leaves (no Children), nothing to do.
		}

		if len(longFaces) == 0 {
			break
		}
		if iter >= MaxIter {
			fmt.Printf("  Warning: subdivision did not fully converge after %d iterations; some faces may be coarser than requested.\n", MaxIter)
			break
		}

		// Mark only the longest edge of each too-long face.
		markedEdges := make(map[[2]uint32]bool, len(longFaces))
		for _, face := range longFaces {
			var bestLen float32
			var bestEdge [2]uint32
			for e := 0; e < 3; e++ {
				a, b := face[e], face[(e+1)%3]
				if l := edgeLen(verts, a, b); l > bestLen {
					bestLen = l
					bestEdge = makeEdge(a, b)
				}
			}
			markedEdges[bestEdge] = true
		}

		// Budget check: each marked edge produces exactly one new vertex.
		estimated := len(verts) + len(markedEdges)
		if estimated > maxVertices {
			return nil, nil, nil, &TooManyVerticesError{Estimated: estimated}
		}

		// Create midpoint vertices for each marked edge.
		edgeToMid := make(map[[2]uint32]uint32, len(markedEdges))
		for e := range markedEdges {
			mid := uint32(len(verts))
			edgeToMid[e] = mid
			a, b := e[0], e[1]
			verts = append(verts, [3]float32{
				(verts[a][0] + verts[b][0]) * 0.5,
				(verts[a][1] + verts[b][1]) * 0.5,
				(verts[a][2] + verts[b][2]) * 0.5,
			})
			uvs = append(uvs, [2]float32{
				(uvs[a][0] + uvs[b][0]) * 0.5,
				(uvs[a][1] + uvs[b][1]) * 0.5,
			})
		}

		// Rebuild too-long faces into children and link them to the tree.
		newNodes := make([]*Node, 0, len(longFaces)*2)
		newFaces := make([][3]uint32, 0, len(longFaces)*2)
		newFaceTex := make([]int32, 0, len(longFaceTex)*2)

		addChild := func(parent *Node, face [3]uint32, tex int32) {
			child := &Node{Face: face, TexIdx: tex}
			parent.Children = append(parent.Children, child)
			newNodes = append(newNodes, child)
			newFaces = append(newFaces, face)
			newFaceTex = append(newFaceTex, tex)
		}

		for fi, face := range longFaces {
			tex := longFaceTex[fi]
			parent := longNodes[fi]
			edges := [3][2]uint32{
				makeEdge(face[0], face[1]),
				makeEdge(face[1], face[2]),
				makeEdge(face[2], face[0]),
			}

			var splitMask [3]bool
			splitCount := 0
			for e := 0; e < 3; e++ {
				if markedEdges[edges[e]] {
					splitMask[e] = true
					splitCount++
				}
			}

			switch splitCount {
			case 1:
				for e := 0; e < 3; e++ {
					if splitMask[e] {
						va, vb, vc := face[e], face[(e+1)%3], face[(e+2)%3]
						mid := edgeToMid[edges[e]]
						addChild(parent, [3]uint32{va, mid, vc}, tex)
						addChild(parent, [3]uint32{mid, vb, vc}, tex)
						break
					}
				}

			case 2:
				for e := 0; e < 3; e++ {
					if !splitMask[e] {
						va := face[e]
						vb := face[(e+1)%3]
						vc := face[(e+2)%3]
						m1 := edgeToMid[edges[(e+1)%3]]
						m2 := edgeToMid[edges[(e+2)%3]]
						addChild(parent, [3]uint32{va, vb, m1}, tex)
						addChild(parent, [3]uint32{va, m1, m2}, tex)
						addChild(parent, [3]uint32{m1, vc, m2}, tex)
						break
					}
				}

			case 3:
				m01 := edgeToMid[edges[0]]
				m12 := edgeToMid[edges[1]]
				m20 := edgeToMid[edges[2]]
				addChild(parent, [3]uint32{face[0], m01, m20}, tex)
				addChild(parent, [3]uint32{m01, face[1], m12}, tex)
				addChild(parent, [3]uint32{m20, m12, face[2]}, tex)
				addChild(parent, [3]uint32{m01, m12, m20}, tex)
			}
		}

		faces = newFaces
		faceTex = newFaceTex
		currentNodes = newNodes
	}

	return roots, verts, uvs, nil
}

// Leaves flattens all leaf nodes of the subdivision tree into a LoadedModel
// suitable for color sampling. Vertices are compacted (only referenced ones
// are kept).
func Leaves(roots []*Node, verts [][3]float32, uvs [][2]float32, model *loader.LoadedModel) *loader.LoadedModel {
	var faces [][3]uint32
	var texIdxs []int32

	var collect func(*Node)
	collect = func(n *Node) {
		if len(n.Children) == 0 {
			faces = append(faces, n.Face)
			texIdxs = append(texIdxs, n.TexIdx)
			return
		}
		for _, c := range n.Children {
			collect(c)
		}
	}
	for _, r := range roots {
		collect(r)
	}

	return compactModel(verts, uvs, faces, texIdxs, model)
}

// Merge performs a bottom-up pass over the subdivision tree. For each internal
// node whose entire subtree maps to a single palette color, the subtree is
// collapsed to that one face. Returns a flat list of (face, color) pairs
// representing the minimal triangle set that preserves color fidelity.
//
// leafColors must be in the same DFS leaf order as produced by Leaves.
func Merge(roots []*Node, leafColors []int32) []FaceColor {
	idx := 0
	var result []FaceColor
	for _, r := range roots {
		collectNode(r, leafColors, &idx, &result)
	}
	return result
}

// collectNode collects faces from n's subtree into result.
// If all faces in the subtree share one color, they are replaced by n's single
// face. Returns the uniform color (or -1 if mixed).
func collectNode(n *Node, leafColors []int32, idx *int, result *[]FaceColor) int32 {
	if len(n.Children) == 0 {
		c := leafColors[*idx]
		*idx++
		*result = append(*result, FaceColor{n.Face, n.TexIdx, c})
		return c
	}

	startLen := len(*result)
	uniform := int32(-2) // -2 = not yet seen

	for _, child := range n.Children {
		c := collectNode(child, leafColors, idx, result)
		if uniform == -2 {
			uniform = c
		} else if c == -1 || c != uniform {
			uniform = -1 // mixed
		}
	}

	if uniform >= 0 {
		// All leaves share the same color: collapse to this node's face.
		*result = (*result)[:startLen]
		*result = append(*result, FaceColor{n.Face, n.TexIdx, uniform})
	}

	return uniform
}

// BuildModel constructs a LoadedModel from a merged face list. Vertices are
// compacted to only those referenced by the merged faces.
func BuildModel(mergedFaces []FaceColor, verts [][3]float32, uvs [][2]float32, model *loader.LoadedModel) (*loader.LoadedModel, []int32) {
	faces := make([][3]uint32, len(mergedFaces))
	texIdxs := make([]int32, len(mergedFaces))
	colors := make([]int32, len(mergedFaces))
	for i, mf := range mergedFaces {
		faces[i] = mf.Face
		texIdxs[i] = mf.TexIdx
		colors[i] = mf.Color
	}
	return compactModel(verts, uvs, faces, texIdxs, model), colors
}

// compactModel builds a LoadedModel from a face list, compacting vertices to
// only those referenced by the faces.
func compactModel(
	verts [][3]float32,
	uvs [][2]float32,
	faces [][3]uint32,
	texIdxs []int32,
	model *loader.LoadedModel,
) *loader.LoadedModel {
	seen := map[uint32]uint32{}
	var newVerts [][3]float32
	var newUVs [][2]float32

	remap := func(idx uint32) uint32 {
		if ni, ok := seen[idx]; ok {
			return ni
		}
		ni := uint32(len(newVerts))
		seen[idx] = ni
		newVerts = append(newVerts, verts[idx])
		newUVs = append(newUVs, uvs[idx])
		return ni
	}

	newFaces := make([][3]uint32, len(faces))
	for fi, f := range faces {
		newFaces[fi] = [3]uint32{remap(f[0]), remap(f[1]), remap(f[2])}
	}

	nTex := len(model.Textures)
	var noTextureMask []bool
	if model.NoTextureMask != nil {
		noTextureMask = make([]bool, len(texIdxs))
		for i, ti := range texIdxs {
			if ti >= int32(nTex) {
				noTextureMask[i] = true
			}
		}
	}

	return &loader.LoadedModel{
		Vertices:       newVerts,
		Faces:          newFaces,
		UVs:            newUVs,
		Textures:       model.Textures,
		FaceTextureIdx: texIdxs,
		NoTextureMask:  noTextureMask,
	}
}
