# DitherForge

![Golden Pheasant printed with DitherForge](images/golden_pheasant.jpg)

Convert textured 3D models (GLB or 3MF) into multi-color 3D-printable files
(3MF) for multi-filament printers.

## Download

Pre-built binaries for Linux, Windows, and macOS are available on the
[Releases](https://github.com/rtwfroody/ditherforge/releases) page.

## Desktop App

DitherForge includes a desktop GUI for interactive model processing.

### Getting Started

1. Launch `ditherforge` (or the installer on Windows)
2. Click **Browse** to select a `.glb` or `.3mf` file
3. Adjust settings in the left panel -- the output preview updates automatically
4. Click **Save** to export the 3MF file
5. Open the result in OrcaSlicer or BambuStudio and print

### Features

**Live 3D preview** -- Input and output models are displayed side by side with
synchronized camera controls. Changes to any setting trigger a new pipeline
run; cached stages are skipped so only the affected steps re-process.

**Color adjustments** -- Brightness, contrast, and saturation sliders adjust
the input model's colors before palette selection. The input preview updates
instantly via GPU shaders; the output re-renders with the adjusted colors.

**Flexible palette** -- Choose the number of colors (1--16), optionally lock
specific colors, and select where the remaining colors come from:
- **Defaults** -- best fit from cyan, magenta, yellow, black, white, red,
  green, blue
- **Inventory** -- pick from a file listing your available filaments
- **Optimal** -- compute dominant colors from the model's texture via k-means

**Color snap** -- Shifts cell colors toward the nearest palette color by a
configurable delta E distance before dithering, reducing noise in nearly
solid-color regions.

### Print Settings

Set **nozzle diameter** and **layer height** to match your slicer settings.
These control the voxel grid resolution and directly affect output quality.

Use **size** to scale the model to a specific extent in mm, or **scale** for a
relative multiplier.

## How It Works

1. **Load** a textured GLB or 3MF model and scale to millimeters.
2. **Voxelize** onto a square grid matching the nozzle diameter and layer
   height, sampling colors from the original texture.
3. **Decimate** the input mesh to reduce triangle count before clipping.
4. **Adjust colors** -- apply brightness, contrast, and saturation.
5. **Build palette** -- resolve locked colors and fill remaining slots from the
   selected source.
6. **Dither** to approximate the full-color texture with the available palette.
   The default `dizzy` mode uses random traversal with error diffusion to
   spatial neighbors, producing blue-noise-like patterns without directional
   bias. `none` assigns the nearest palette color with no dithering.
7. **Clip** the mesh along voxel color boundaries, assigning each fragment a
   palette color.
8. **Merge** coplanar triangles to reduce face count.
9. **Export** a 3MF file with per-face material assignments.

Each stage is cached by its settings, so changing a downstream parameter (e.g.
dithering mode) skips all upstream stages.

## CLI

The `ditherforge-cli` binary provides the same pipeline without a GUI.

```
ditherforge-cli model.glb --size 100
```

This loads `model.glb`, scales it to 100mm, picks 4 colors from the defaults,
and writes `output.3mf`.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--size` | -- | Scale model so largest extent equals this value in mm |
| `--scale` | `1.0` | Additional scale multiplier |
| `-n` | `4` | Number of palette colors |
| `--color` | -- | Lock a color (CSS name or hex, repeatable) |
| `--auto` | -- | Compute remaining colors optimally via k-means |
| `--inventory` | -- | Inventory file for remaining colors |
| `--brightness` | `0` | Brightness adjustment (-100 to +100) |
| `--contrast` | `0` | Contrast adjustment (-100 to +100) |
| `--saturation` | `0` | Saturation adjustment (-100 to +100) |
| `--dither` | `dizzy` | Dithering mode: `none`, `dizzy` |
| `--nozzle-diameter` | `0.4` | Nozzle diameter in mm |
| `--layer-height` | `0.2` | Layer height in mm |
| `--color-snap` | `5` | Shift cell colors toward nearest palette color (delta E, 0 to disable) |
| `--output` | `output.3mf` | Output file path |
| `--no-merge` | -- | Skip coplanar triangle merging |
| `--no-simplify` | -- | Skip mesh decimation before clipping |
| `--force` | -- | Bypass the 300mm extent size check |
| `--stats` | -- | Print face counts per material |

## Building from Source

Requires Go 1.21+ and Node.js 20+.

```
git clone https://github.com/rtwfroody/ditherforge.git
cd ditherforge
./build.sh
```

This builds both `build/bin/ditherforge` (GUI) and `ditherforge-cli` (CLI).
The GUI build requires the [Wails](https://wails.io/) CLI:

```
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

For development with hot reload:

```
wails dev
```

## Recommended Models

These models work well with DitherForge and are free to download:

| Model | Author | Source | License |
|-------|--------|--------|---------|
| Golden Pheasant | iRahulRajput | [Sketchfab](https://sketchfab.com/3d-models/golden-pheasant-f9b3decb485c4a7c9d97cf70b17cbd29) | [CC BY 4.0](http://creativecommons.org/licenses/by/4.0/) |

## Testing

```
go test -timeout 10m ./...
```

## Known Issues

- Sometimes generates features too thin for the slicer to print.

## Status

Early development. The output 3MF includes embedded printer profiles for the
Snapmaker U1 with a 0.4mm nozzle. Other printers may need manual profile
adjustment in the slicer.
