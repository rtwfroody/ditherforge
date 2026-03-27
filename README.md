# ditherforge

Convert textured 3D models (GLB) into multi-color 3D-printable files (3MF) for
multi-filament printers. ditherforge maps the original texture onto a limited
filament palette using Floyd-Steinberg dithering, producing files ready for
slicers like OrcaSlicer and BambuStudio.

## How It Works

1. **Load** a textured GLB model and scale to millimeters.
2. **Voxelize** onto a hexagonal prism grid matching the printer's nozzle
   diameter and layer height (hexvoxel mode), or **subdivide** the original mesh
   to a target resolution (subdivide mode).
3. **Extract** a smooth isosurface via marching prisms (hexvoxel) or merge
   uniform coplanar regions (subdivide).
4. **Sample** the original texture color at each output face.
5. **Dither** using Floyd-Steinberg error diffusion in Morton-curve order to
   approximate the full-color texture with the available filament palette.
6. **Export** a 3MF file with per-face paint colors and embedded slicer profiles
   (Snapmaker U1 / OrcaSlicer compatible).

## Installation

Requires Go 1.21+.

```
go install github.com/rtwfroody/ditherforge@latest
```

Or build from source:

```
git clone https://github.com/rtwfroody/ditherforge.git
cd ditherforge
go build .
```

## Usage

```
ditherforge <input.glb> [options]
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--palette` | `white,cyan,magenta,yellow` | Comma-separated colors (CSS names or `#hex`) |
| `--auto-palette N` | — | Compute N dominant colors from texture instead of manual palette |
| `--mode` | `hexvoxel` | Remesh mode: `hexvoxel` or `subdivide` |
| `--nozzle-diameter` | `0.4` | Nozzle diameter in mm (hexvoxel mode) |
| `--layer-height` | `0.2` | Layer height in mm (hexvoxel mode) |
| `--resolution` | `0.5` | Target max edge length in mm (subdivide mode) |
| `--glb-unit` | `m` | GLB coordinate unit: `m`, `dm`, `cm`, `mm` |
| `--scale` | `1.0` | Additional scale multiplier |
| `--output` | `output.3mf` | Output 3MF file path |
| `--color-space` | `cielab` | Color distance metric: `cielab` or `rgb` |
| `--no-dither` | — | Disable Floyd-Steinberg dithering (nearest-color only) |
| `--no-merge` | — | Skip coplanar triangle merging |
| `--stats` | — | Print face counts per material |
| `--force` | — | Bypass the 300 mm extent size check |

### Examples

Basic usage with a custom palette:
```
ditherforge model.glb --palette "gray,black,white,red" --scale 0.13
```

Auto-detect 4 dominant colors from the texture:
```
ditherforge model.glb --auto-palette 4
```

Model already in millimeters:
```
ditherforge model.glb --glb-unit mm
```

Use subdivide mode for models where preserving the original mesh topology matters:
```
ditherforge model.glb --mode subdivide --resolution 0.3
```

## Printing

The output 3MF includes embedded printer and process profiles for the Snapmaker
U1 with a 0.4 mm nozzle. It can be opened directly in OrcaSlicer or
BambuStudio. The file uses per-face paint colors, so the slicer handles
filament changes automatically via its AMS/multi-material system.

## Testing

Silhouette-based regression tests compare the output shape against the original
model from multiple views. See [tests/README.md](tests/README.md) for details.

```
uv run --with trimesh --with pillow --with numpy python3 tests/test_silhouette.py
```

## Status

Early development. Hexvoxel mode produces good results for many models.
Subdivide mode works but produces larger files. The embedded slicer profiles
target the Snapmaker U1; other printers may need manual profile adjustment in
the slicer.
