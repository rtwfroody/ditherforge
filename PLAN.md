# text2filament MVP Implementation Plan

## What's Done

- `loader.py` — loads GLB (single or multi-mesh with shared texture), extracts vertices, UVs, texture image
- `__init__.py` — CLI skeleton with all flags wired up via argparse

## Implementation Order

### 1. `palette.py` — pure functions, no dependencies

**Inputs/outputs:**
- `parse_palette(hex_colors: list[str]) -> np.ndarray` — `(P, 3)` uint8 RGB
- `assign_palette(face_colors_rgb, palette_rgb, color_space) -> np.ndarray` — `(F,)` int indices

**Key details:**
- `colorspacious.cspace_convert(arr, "sRGB255", "CIELab")` for batch LAB conversion (vectorized)
- Nearest-neighbor: `(F, 1, 3) - (1, P, 3)` broadcast → `(F, P)` distances → `argmin(axis=1)`
- RGB fallback skips cspace_convert, operates on float-cast uint8 directly

---

### 2. `sample.py` — depends only on LoadedModel

**Inputs/outputs:**
- `sample_face_colors(model: LoadedModel) -> np.ndarray` — `(F, 3)` uint8 RGB

**Key details:**
- `face_uvs = model.uvs[model.mesh.faces]` → `(F, 3, 2)`, mean over axis=1 → `(F, 2)` centroid UVs
- **Y-flip required:** `py = (1 - v) * (H - 1)` — UV (0,0) is bottom-left, PIL (0,0) is top-left
- Vectorized bilinear sampling with numpy (not Pillow.getpixel — too slow)
- Clamp UVs to [0,1] before sampling

---

### 3. `subdivide.py` — most algorithmically involved

**Inputs/outputs:**
- `subdivide(model: LoadedModel, max_edge_mm: float) -> LoadedModel`

**Key details:**
- Cannot use `trimesh.remesh.subdivide_to_size()` — it doesn't support `vertex_attributes`
- Loop manually: each iteration calls `remesh.subdivide(verts, too_long_faces, vertex_attributes={"uv": uvs})`
  - trimesh averages UV at each new midpoint vertex automatically
- Accumulate "done" faces (those already under threshold) separately each iteration
- Combine at end: `trimesh.Trimesh(vertices=final_verts, faces=final_faces, process=False)`
- **Never merge vertices by position** — UV seams require split vertices to remain split

---

### 4. `export_3mf.py` — write XML manually

**Inputs/outputs:**
- `export_3mf(model, assignments, palette_hex, output_path) -> None`

**Key details:**
- trimesh's `export_3MF` doesn't write `<basematerials>` or per-triangle material — must build XML manually
- Structure: zip containing `3D/3dmodel.model`, `_rels/.rels`, `[Content_Types].xml`
- Materials namespace: `xmlns:m="http://schemas.microsoft.com/3dmanufacturing/material/2015/02"`
- `<m:basematerials id="1">` with one `<m:base>` per palette color
- `m:displaycolor` must be `#RRGGBBAA` — append `FF` to user's 6-digit hex
- Each `<triangle v1 v2 v3 pid="1" p1="N" p2="N" p3="N"/>` — all three corners same index for solid face color
- Use string formatting (not ElementTree) for performance at 500k faces

---

### 5. `preview.py` — trivial

**Inputs/outputs:**
- `export_preview(model, assignments, palette_rgb, output_path) -> None`

**Key details:**
- `face_colors = palette_rgb[assignments]` — numpy index into palette, shape `(F, 3)`
- Append alpha=255 column → `(F, 4)` uint8
- Assign `trimesh.visual.ColorVisuals(mesh=model.mesh, face_colors=face_colors_rgba)`
- `model.mesh.export(output_path)` — use `.ply` extension

---

### 6. Wire `__init__.py`

Replace `print("(Pipeline not yet implemented)")` with:

```
load -> subdivide -> sample -> assign_palette -> [--preview] -> export_3mf
```

Add `--stats` output: face count per material after assignment.

---

## Integration Invariants

- `model.uvs` is always `(N, 2)` parallel to `model.mesh.vertices` — never reorder/merge vertices
- `assignments` is `(F,)` int64 with values `[0, P)` — produced by palette, consumed by export and preview
- 3MF `pid`/`p1`/`p2`/`p3` are in core namespace (no prefix); `basematerials`/`base` use `m:` prefix
