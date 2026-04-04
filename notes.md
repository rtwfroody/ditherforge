# Development Notes

## Mesh Simplification Approaches (branch: shared-edge-vertices)

### Goal
Simplify the voxelized output mesh (reduce triangle count from ~3.4M to ~800K)
while maintaining watertightness — every directed half-edge A→B has exactly one
matching B→A.

### Test infrastructure
- `CheckWatertight()` in `internal/voxel/watertight.go` — counts boundary edges
  (no matching reverse) and non-manifold edges (>1 matching reverse).
- `mesh_render_test.go` — loads test models from `objects/`, runs remesh, checks
  watertight if input is watertight. Uses `--size 50` for speed. Caches remesh
  results keyed on model mod time + config hash.
- Test model: `charizard-fs.3mf` (watertight input, 1.5M faces, non-free license
  so gitignored).

### Approach 1: Shared Edge Vertices (abandoned)
Each cell gets a single averaged plane. Shared vertices are computed at voxel
grid edges by intersecting neighboring cells' planes with each edge. Polygons
built from shared vertices, fan-triangulated.

**Result**: ~85-88% triangle reduction but still has gaps. Cells compute geometry
independently → different cells sharing a face produce mismatched edges.

**Root cause**: Each cell independently determines its own plane, so adjacent
cells get different planes that don't produce the same geometry on their shared
face.

### Approach 2: Per-Cell Convex Hull (abandoned)
Collect all boundary vertices (onBound >= 1) per cell, compute convex hull,
fan-triangulate.

**Result**: 1.9M faces (vs 3.4M unsimplified). 735K boundary edges, 33K
non-manifold. Same root cause — each cell independently computes geometry.

### Approach 3: Per-Face Hull + Per-Cell Angle Sort (abandoned)
Phase 2a: Compute 2D convex hull on each voxel FACE (shared between 2 cells).
Phase 2b: Collect hull vertices from cell's 6 faces, sort by angle around
accumulated surface normal, fan-triangulate.

**Result**: 299K boundary, 232K non-manifold. Angle sort is the problem —
`sortPolygonVerts` projects onto a plane perpendicular to the cell's accumulated
normal. Different cells have different normals → different sort orders →
mismatched edges on shared faces.

### Approach 4: Per-Face Hull + Boundary Tracing (current)
Phase 2a: Same per-face hull computation.
Phase 2b: Trace boundary polygon by walking face hull edges from face to face.
At cell-edge vertices (2+ coords on cell boundaries), the polygon transitions
from one face hull to another. Convention: HIGH face = hull as-is (CCW from
+axis), LOW face = hull reversed. Adjacent cells traverse shared hull in
opposite directions → edges should match.

**Result**: 131K boundary, 13K non-manifold. Big improvement on non-manifold.
~98% of cells trace successfully, 1.7% fall back to emitDirect.

**Remaining issues**:
1. **Fallback cells** (~780/45500): Tracing fails when a transition vertex
   appears on one hull but not the adjacent face's hull (collinear vertex
   eliminated by convex hull), or when the polygon is degenerate (polyLen < 3,
   transitions adjacent on hull). These cells emit raw fragments → mismatched
   edges with neighbors.
2. **Segment mismatch**: Two cells sharing a face may use different segments of
   the face hull if the surface intersects the face in multiple curves or at
   different transition vertices. Cell A uses T1→T2, cell B uses T2→T3 → edges
   on the shared face don't match.
3. **Winding**: Can't reverse polygon for winding correction without reversing
   all perimeter edges, which breaks the edge matching with adjacent cells.
   Convention determines winding; must be correct from the start.

**Attempted fixes**:
- Keeping collinear points in convex hull (`<= 0` → `< 0`): Inflated face
  count without fixing the real problem.
- Post-processing hulls to reinsert eliminated cell-edge vertices: Marginal
  improvement (~20 fewer failures).

### Key Learnings

1. **Watertightness requires shared computation**: Any approach where each cell
   independently computes its geometry (convex hull, angle sort, plane
   intersection) will produce mismatched edges on shared faces. The geometry on
   a shared face MUST be computed once and used identically (reversed) by both
   cells.

2. **Per-face hulls are the right shared unit**: Computing the convex hull on
   each voxel face and sharing it between adjacent cells is the correct
   foundation. The challenge is assembling these face hulls into per-cell
   polygons without introducing independent computation.

3. **Boundary tracing works for simple cases**: When the surface intersects each
   face as a single curve with exactly 2 transition vertices, the tracing
   produces matching segments and is watertight. The failures come from complex
   cases (multiple intersections, degenerate slivers, corner vertices).

4. **The convex hull eliminates needed vertices**: Andrew's monotone chain with
   `cross <= 0` removes collinear points. Cell-edge vertices that are collinear
   with hull vertices get eliminated, breaking the tracing at those transitions.

5. **Fan triangulation winding is locked to perimeter direction**: The perimeter
   edges determine both the polygon shape AND its winding. You can't change
   winding without changing edges, and changing edges breaks watertightness.

### Unexplored options

1. **Emit face hull polygons directly**: Triangulate each face hull once, assign
   half to each adjacent cell (opposite winding). Cell "surface" becomes flat
   patches on cell faces. Watertight by construction, but blocky/faceted
   appearance.

2. **Constrained cell assembly**: For each shared face, coordinate between both
   cells to agree on which segment of the face hull they'll use. Build cell
   polygons from agreed-upon segments only.

3. **Hybrid**: Trace where possible, emit face hulls for fallback cells (at
   least the shared face edges match, even if the cell interior isn't filled).

### Watertight Results Summary (charizard-fs.3mf, --size 50)

| Approach | Boundary edges | Non-manifold | Faces |
|---|---|---|---|
| No simplify (baseline) | ~20K | ~83K | 3.4M |
| Per-cell convex hull | 735K | 33K | 1.9M |
| Per-face hull + angle sort | 299K | 232K | ~793K |
| Per-face hull + boundary trace | 131K | 13K | ~791K |
