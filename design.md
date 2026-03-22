# tex2filament — Design Document

## Overview

**tex2filament** is a command-line tool that converts a textured 3D model into a multi-material 3MF file ready for slicing in OrcaSlicer (or compatible slicers). It maps continuous-color UV textures down to a user-specified palette of filament colors, assigning one material per mesh face.

The core insight: FDM multi-material printers can only deposit discrete filament colors, not continuous color. This tool bridges the gap between "artist paints a texture" and "printer lays down filament," operating directly on the 3D mesh rather than flattening to 2D and back.

## Problem Statement

Today there is no open, turnkey pipeline for: textured 3D model → filament-palette color assignment → sliceable multi-material file. The pieces exist independently (UV texturing in Blender, 2D palette reduction in GIMP, multi-body 3MF import in Orca), but stitching them together requires manual scripting per model. Meshy offers a cloud-based solution locked to Bambu Lab's ecosystem. The Grasshopper/Rhino approach demonstrated by "Medium Things" produces remarkable dithered prints but bypasses the slicer entirely with custom gcode, and isn't packaged as a reusable tool.

## Target Users

Makers with multi-material FDM printers (tool changers like the Snapmaker U1, Prusa XL; or AMS/MMU systems) who want to print textured models in multiple filament colors without manually painting every face in the slicer.

## MVP Scope

### Input

- A 3D model file with UV-mapped texture. Supported formats: OBJ + MTL + texture image (PNG/JPG), or GLB/GLTF with embedded texture.
- A palette definition: a list of filament colors as hex RGB values, e.g. `--palette "#FFFFFF,#000000,#CC0000,#FFD700"`.
- A target maximum edge length in mm (default: 1.0mm), controlling color resolution.

### Processing Pipeline

**Step 1 — Load mesh and texture.**
Parse the model, extract vertex positions, UV coordinates, and the associated texture image. Validate that UVs exist and the texture is readable.

**Step 2 — Adaptive subdivision.**
Subdivide mesh triangles until no edge exceeds the target length. Interpolate UV coordinates during subdivision to preserve texture mapping. Use midpoint subdivision (split longest edge, propagate to neighbors to maintain a conforming mesh). This step increases face count to ensure each face is small enough to represent a single color at the desired resolution.

**Step 3 — Per-face color sampling.**
For each face, compute the centroid in UV space by averaging the UV coordinates of its three vertices. Sample the texture image at that UV coordinate (bilinear interpolation). This yields an RGB color per face.

**Step 4 — Palette mapping.**
For each face's sampled RGB color, find the nearest color in the user-defined palette. Distance metric: Euclidean distance in CIELAB color space (perceptually uniform, significantly better than raw RGB distance for this use case). Assign the corresponding material/extruder index to that face.

**Step 5 — Export 3MF.**
Write a 3MF file with per-face material assignments. The 3MF format natively supports multiple materials via `<basematerials>` and per-triangle `pid`/`pindex` attributes in the mesh definition. OrcaSlicer, Bambu Studio, and PrusaSlicer all read these.

### Output

A single `.3mf` file where each triangle is assigned to one of N material slots corresponding to the input palette colors. Opening this file in OrcaSlicer should show the model with color regions already assigned to filament slots — no manual painting required.

### Command-Line Interface

```
tex2filament input.obj \
  --palette "#FFFFFF,#000000,#CC0000,#FFD700" \
  --resolution 1.0 \
  --output model_colored.3mf
```

Optional flags:
- `--color-space rgb` — use RGB distance instead of CIELAB (faster, worse perceptual results).
- `--preview` — export a colored OBJ/PLY for visual inspection before committing to 3MF.
- `--stats` — print face count per material, total face count, and mesh size info.

## Technical Decisions

### Language: Python

The ecosystem is strongest here for this task: `trimesh` for mesh I/O and manipulation, `Pillow` for texture sampling, `numpy` for vectorized math, `colorspacious` or `colour-science` for CIELAB conversion. The tool is not performance-critical in the way a slicer is — processing a mesh of a few hundred thousand faces should take seconds, not minutes.

### Subdivision Strategy

Midpoint subdivision splitting the longest edge, iterated until all edges are below threshold. This is simple and produces reasonable triangle quality. We do not need Loop subdivision or Catmull-Clark smoothing here — we are not trying to smooth the geometry, just increase face density for color resolution. The geometry should remain faithful to the original surface.

Key consideration: subdivision must interpolate UVs, not just positions. Each new vertex created at an edge midpoint gets UV coordinates that are the average of its two parent vertices' UVs. This preserves the texture mapping through subdivision.

### Why CIELAB for Color Matching

RGB Euclidean distance is a poor proxy for perceptual color difference. Two colors that look very different to a human (e.g., dark blue and dark green) can be close in RGB, while colors that look similar (e.g., two slightly different yellows) can be far apart. CIELAB is designed so that Euclidean distance in the space corresponds to perceived color difference. For mapping to a small palette (4–8 colors), this makes a meaningful difference in which faces get assigned to which filament.

### 3MF Format Details

The 3MF Materials Extension (`http://schemas.microsoft.com/3dmanufacturing/material/2015/02`) defines `<basematerials>` groups. Each face in the `<triangles>` element can reference a material via `pid` (pointing to the basematerials group) and `pindex` (index within that group). This is the standard way to express per-face material assignment in 3MF and is supported by all major slicers.

We will also set the display color of each basematerial to match the user's palette hex values, so the model previews correctly in the slicer.

### Mesh Quality After Subdivision

Subdividing to 1mm edges on a 100mm object will produce roughly 10,000–500,000 faces depending on surface area and geometry. This is well within what modern slicers handle. The faces will be small and regular, which slicers actually prefer over large irregular triangles.

## Architecture

```
tex2filament/
├── __main__.py          # CLI entry point (argparse)
├── loader.py            # Load OBJ/GLB, extract mesh + UVs + texture
├── subdivide.py         # Adaptive edge-length subdivision with UV interpolation
├── sample.py            # Per-face texture sampling (centroid in UV space)
├── palette.py           # CIELAB conversion, nearest-palette-color matching
├── export_3mf.py        # Write 3MF with per-face materials
└── preview.py           # Optional colored OBJ/PLY export for debugging
```

## Limitations and Known Issues

**Texture seams.** Models with UV seams may have discontinuities where the same geometric edge has different UV coordinates on each side. Subdivision at seams must handle this carefully — the two sides must remain geometrically coincident but can have different UVs. `trimesh` tracks this via separate entries in the vertex array for split UVs.

**No texture wrapping/tiling.** MVP assumes UVs are in [0,1]. Tiled or mirrored textures would need UV coordinate normalization.

**Single texture only.** Models with multiple texture maps (e.g., separate maps for different parts) will need to either be pre-merged or handled by processing each material group separately.

**Palette limited by printer.** Most multi-material setups support 4–8 colors. The tool imposes no limit, but practical printing does.

**Color accuracy.** The palette hex values are an approximation of how the filament actually looks when printed. Filament color varies with layer height, temperature, and lighting. This is a calibration problem outside our scope.

## Future Features

### Dithering

The most impactful enhancement. Instead of snapping each face to the single nearest palette color, apply Floyd-Steinberg or halftone dithering in the mesh domain. This would allow the illusion of color mixing — a region that should be orange could alternate between red and yellow faces. The challenge is defining "neighboring face" traversal order for error diffusion, since unlike a 2D pixel grid, mesh faces don't have a natural scanline order. Approaches include:

- Hilbert-curve traversal of face centroids projected to 2D (UV space or a surface parameterization).
- Operating on the baked UV texture image in 2D (dither the flat texture, then map back) — simpler but reintroduces the UV distortion issue.
- Blue-noise threshold dithering, which is order-independent and may be more natural for mesh faces.

### Triangle Merging / Mesh Simplification

Recombine small same-color adjacent triangles back into larger faces where the surface is approximately planar. This reduces file size and may improve slicer performance. Approach: iterative edge collapse within same-material regions, constrained to preserve color boundaries and surface accuracy (bounded error decimation). Low priority since slicers handle high face counts fine.

### Texture-Aware Adaptive Resolution

Instead of uniform subdivision to a fixed edge length, analyze the texture gradient magnitude per face and only subdivide where the texture has detail. Large regions of uniform color don't need small triangles. This would significantly reduce face count on models with simple textures.

### Multi-Texture / PBR Support

Handle models with multiple texture maps, including PBR materials where the base color map is separate from roughness/normal maps. Only the base color (albedo) map matters for filament assignment, but the loader needs to identify and extract the correct one.

### Interactive Palette Tuning

A preview mode (possibly a simple web UI or Blender add-on) where you can see the model with the quantized colors applied, adjust palette entries, and see the result update in real time before committing to 3MF export.

### Slicer-Specific Paint Data

Instead of (or in addition to) per-face material via the 3MF materials extension, write slicer-specific paint data. PrusaSlicer and OrcaSlicer use `slic3rpe:mmu_segmentation` attributes in the 3MF, which encode sub-triangle paint regions that can be finer than individual mesh faces. Supporting this format could allow color detail finer than mesh resolution, at the cost of slicer lock-in.

### Halftone Patterns for Surface Texture

Beyond color dithering, use patterned material alternation to create visual texture effects — e.g., a wood grain appearance using two brown filaments in a striped pattern. This crosses over from color reproduction into decorative surface treatment.

### Color Separation Reporting

Output a per-filament usage estimate (approximate surface area and volume per color) so the user can predict filament consumption and swap timing.

### Blender Add-On

Package the pipeline as a Blender add-on with UI panels for palette definition, resolution control, preview rendering, and direct 3MF export. This removes the command-line step and lets users iterate within their modeling environment.
