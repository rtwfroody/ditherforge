# Development Notes

## Shared Edge Vertices (branch: shared-edge-vertices)

### Goal
Replace per-cell convex hull simplification with single-plane-per-cell
simplification using shared edge vertices for watertightness.

### Approach
1. **Pass 1**: Clip original mesh triangles against voxel boundaries (unchanged).
   Accumulate area-weighted normals and centroids per cell in float64.
2. **Pass 2**: For each cell, resolve a single plane (averaged normal + centroid).
   Pre-compute shared vertices at voxel grid edges by averaging where neighboring
   cells' planes intersect each edge. Build polygons from these shared vertices
   and fan-triangulate.

### Key data structures
- `cellAccum`: accumulates area-weighted normal, centroid, and total area per cell.
- `edgeKey{axis, aCell, bLine, cLine}`: canonical identifier for a box edge.
  The edge is parallel to `axis`, spans cell `aCell`, at grid lines `bLine` and
  `cLine` on the other two axes.
- `edgeVert`: stores the shared vertex position and a `cells [4]bool` tracking
  which of the 4 neighboring cells contributed (their plane intersected the edge
  with t in [0,1]).
- `edgeNeighborCK(ek, dbb, dcc)`: maps neighbor slot (dbb,dcc) to a CellKey,
  handling axis remapping.

### Neighbor slot indexing
The 4 cells sharing an edge are at `(bLine-1+dbb, cLine-1+dcc)` for dbb,dcc
in {0,1}. When iterating edges from a cell's perspective with `(db, dc)` where
`bLine = coords[b]+db`, the current cell occupies slot `(1-db)*2+(1-dc)`, NOT
`db*2+dc`. This was a bug that caused asymmetric vertex inclusion between
adjacent cells.

### Current status
- Triangle count reduction: ~85-88% vs unsimplified mesh.
- Tests pass.
- Still has non-manifold edges / gaps. Possible causes:
  - Cells at the surface boundary that have no neighbors on some edges.
  - The relaxed tolerance fallback (t in [-0.1, 1.1]) for non-contributing cells
    may include vertices that the neighbor doesn't include, breaking symmetry.
  - Polygon vertex ordering via angle sort may produce incorrect winding for
    non-convex polygons or nearly-degenerate cases.
  - Cells with very oblique planes may produce fewer than 3 edge intersections,
    getting skipped entirely and leaving holes.

### Removed code
- `convexHull2D`: no longer needed (was used by old per-cell convex hull).
- `cellBounds`: was unused after the rewrite.
- `clipPlaneByCell`, `clipPolygonByHalfPlane`: replaced by direct polygon
  construction from shared vertices.
