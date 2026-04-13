# DitherForge

![Golden Pheasant printed with DitherForge](images/golden_pheasant.jpg)

Convert textured 3D models (GLB or 3MF) into multi-color 3D-printable files
(3MF) for multi-filament printers.

## Download

Pre-built binaries for Linux, Windows, and macOS are available on the
[Releases](https://github.com/rtwfroody/ditherforge/releases) page.

## Quick Start

1. Launch `ditherforge`
2. Use **File > Open** to select a `.glb` or `.3mf` file
3. Set **Nozzle diameter** and **Layer height** to match your slicer
4. Set **Size (mm)** to your target print size
5. Optionally, open the **Stickers** panel to apply PNG images onto the model surface
6. Adjust the palette and color settings — the output preview updates automatically
7. Use **File > Export 3MF** to save the result
8. Open the exported 3MF in OrcaSlicer or BambuStudio and print

For real use, you'll want to update your Inventory filament collection as
described right below.

---

## How to Manage Filament Collections

The **Filaments** menu lists all available filament collections. Click a
collection name to open its editor.

In the collection editor you can:

- Add, edit, or remove colors (click a swatch to change its hex or label)
- Delete the collection

Use **Filaments > Import...** to load a collection from a plain-text file.
Each line must be in the format `#RRGGBB Label`, for example:

```
#FF0000 Red
#00FF00 Green
#0000FF Blue
```

Use **Filaments > New...** to create an empty collection and add colors
manually.

A built-in **Panchroma Basic** collection (28 colors) is included and cannot
be deleted.

## How to Set Print Dimensions

Use **Size (mm)** to scale the model so its largest extent matches the given
value. For example, set `100` to make the model 100 mm wide (or tall, whichever
is larger).

Use **Scale** mode for a relative multiplier instead. Toggle between Size and
Scale using the radio buttons above the input field.

## How to Set Nozzle and Layer Height

Set **Nozzle diameter** and **Layer height** in the settings panel to match the
values you will use in your slicer. These control the voxel grid resolution:

- The first layer uses wider voxels (`nozzle × 1.275`) to ensure full coverage
  and prevent the slicer from dropping thin features.
- Upper layers use narrower voxels (`nozzle × 1.05`) for finer color detail.

The default values are 0.4 mm nozzle and 0.20 mm layer height.

## How to Configure the Color Palette

The palette grid shows all color slots. Each slot is either:

- **Locked** (solid border, lock icon) — a specific filament you have chosen
- **Unlocked** (dashed border) — filled automatically from the active filament
  collection

Click a slot to open the collection picker and choose a filament color. This
locks the slot to that color. To unlock it and return it to auto, click the
lock icon in the top-left corner of the swatch.

Add slots with the **+** button (up to 16). Remove a slot with the **×** button
that appears on hover. The number of slots is the total number of filaments
used in the output.

### Unlocked colors

Unlocked slots are filled with the best-matching colors from the filament
collection selected under **Unlocked colors from**. Locked colors are taken
into account, so auto-selected colors complement rather than duplicate them.

Use **Filaments** in the menu bar to manage collections. See [Managing Filament
Collections](#how-to-manage-filament-collections).

## How to Adjust Colors

Three sliders adjust the model's colors before palette selection:

- **Brightness** — lighten or darken (-100 to +100, default 0)
- **Contrast** — increase or reduce contrast (-100 to +100, default 0)
- **Saturation** — increase or reduce color intensity (-100 to +100, default 0)

The input preview reflects these adjustments instantly via GPU shaders. The
output re-renders with each change.

## How to Use Stickers

Stickers let you apply PNG images directly onto the model surface before
voxelization. The image is projected along the surface topology, so it wraps
around curves naturally rather than being applied as a flat decal.

To place a sticker:

1. Open the **Stickers** panel in the sidebar.
2. Click **Add Sticker** and choose a PNG file.
3. Click a point on the input model to place it. The sticker centers on that
   point, oriented to the surface.
4. Adjust **Scale** and **Rotation** as needed.

To add more stickers, repeat the process. There is no hard limit on the number
of stickers.

Stickers are composited over the base model color during voxelization. They are
affected by brightness, contrast, and saturation adjustments like any other
color on the model.

Sticker placements are saved and restored with the JSON settings file.

## How to Use Color Pins

Color pins remap specific colors in the model before dithering. Use them to
correct individual colors without affecting the rest of the model — for example,
to shift a too-yellow green toward a truer green filament.

Each pin has:

- **Source color** — the color to replace, sampled from the input model or
  typed as `#RRGGBB`
- **Target color** — the filament color to map toward, chosen from a collection
- **Reach** — how far the adjustment spreads in color space (delta E units,
  default 5). Higher values affect a broader range of similar colors.

To sample a source color from the model, click the eyedropper icon on a pin
and then click a point on the input model preview. The color at that pixel is
captured as the source.

Up to 8 pins are supported. The pipeline uses Gaussian RBF interpolation in
CIELAB color space to blend multiple pin effects smoothly.

## How to Use Color Snap

**Color snap** shifts each voxel's color toward the nearest palette color before
dithering, by up to the configured delta E distance. This reduces noise in
regions that are nearly a single solid color.

Set the value with the **Color snap (delta E)** slider (0 to 50, default 5).
Set to 0 to disable.

## How to Save and Load Settings

Use **File > Save JSON** to save all current settings — palette, color pins,
adjustments, size, and nozzle settings — to a JSON file.

Use **File > Save JSON As...** to save to a new file.

Use **File > Open** and select a `.json` file to restore all settings and
re-open the associated model.

Settings files are automatically associated with the input model. When you open
a model, DitherForge suggests a default settings path based on the model's
filename.

## How to Export a 3MF

Use **File > Export 3MF...** after the pipeline finishes (the output preview
is visible). The exported file includes per-face material assignments
compatible with OrcaSlicer and BambuStudio.

**File > Export 3MF** is disabled until the pipeline produces a valid output.

---

## How It Works

1. **Load** — reads a GLB or 3MF file and scales it to millimeters. The model
   bottom is normalized to Z = 0.
2. **Stickers** — composites any sticker images onto the model's texture. Each
   sticker is placed at a clicked surface point and projected outward following
   the mesh topology.
3. **Voxelize** — maps the model onto a grid of cells matching the nozzle and
   layer settings. Each cell gets the color sampled from the original texture
   (including any stickers). First-layer cells are wider (`nozzle × 1.275`);
   upper cells are narrower (`nozzle × 1.05`).
4. **Decimate** — reduces the triangle count of the input mesh before clipping,
   using QEM mesh decimation.
5. **Color adjust** — applies brightness, contrast, and saturation.
6. **Color warp** — applies color pin remappings using Gaussian RBF
   interpolation in CIELAB color space.
7. **Palette** — resolves locked colors, then selects auto colors from the
   active collection. Applies color snap to shift cell colors toward the palette.
8. **Dither** — assigns a palette color to each cell to approximate the original
   texture. The default `dizzy` mode uses random traversal with error diffusion
   to spatial neighbors, producing blue-noise-like patterns. `none` assigns the
   nearest palette color with no dithering.
9. **Clip** — cuts the decimated mesh along voxel color boundaries and assigns
   each fragment a palette color.
10. **Merge** — merges coplanar triangles to reduce face count.
11. **Export** — writes a 3MF file with per-face material assignments.

Each stage is cached by its settings hash. Changing a downstream parameter
(e.g., dithering mode) skips all upstream stages on the next run.

---

## CLI

`ditherforge-cli` provides the same pipeline from the command line, without a
GUI window.

```
ditherforge-cli model.glb --size 100
```

This loads `model.glb`, scales it to 100 mm, selects 4 colors from the default
palette, and writes `output.3mf`.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--size` | — | Scale model so largest extent equals this value in mm |
| `--scale` | `1.0` | Scale multiplier (applied on top of unit conversion) |
| `-n` | `4` | Number of palette colors |
| `--color` | — | Lock a color (CSS name or `#RRGGBB`; repeatable, comma-separated) |
| `--inventory` | — | Filament inventory file (`#RRGGBB Label` per line) for auto colors |
| `--brightness` | `0` | Brightness adjustment (-100 to +100) |
| `--contrast` | `0` | Contrast adjustment (-100 to +100) |
| `--saturation` | `0` | Saturation adjustment (-100 to +100) |
| `--dither` | `dizzy` | Dithering mode: `none` or `dizzy` |
| `--nozzle-diameter` | `0.4` | Nozzle diameter in mm |
| `--layer-height` | `0.2` | Layer height in mm |
| `--color-snap` | `5` | Pre-dither color snap distance in delta E (0 to disable) |
| `--output` | `output.3mf` | Output file path |
| `--no-merge` | — | Skip coplanar triangle merging |
| `--no-simplify` | — | Skip QEM mesh decimation before clipping |
| `--force` | — | Bypass the 300 mm extent size check |
| `--stats` | — | Print face counts per material |

When `--inventory` is not specified, the CLI selects from a built-in set of
basic colors (cyan, magenta, yellow, black, white, red, green, blue).

---

## Building from Source

Requires Go 1.21+, Node.js 20+, and the [Wails](https://wails.io/) CLI.

Install Wails:

```
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

Build both binaries:

```
git clone https://github.com/rtwfroody/ditherforge.git
cd ditherforge
./build.sh
```

This produces:
- `build/bin/ditherforge` — desktop GUI
- `ditherforge-cli` — CLI tool

For development with hot reload:

```
wails dev
```

## Testing

```
go test -timeout 10m ./...
```

---

## Recommended Models

These models work well with DitherForge and are free to download:

| Model | Author | Source | License |
|-------|--------|--------|---------|
| Golden Pheasant | iRahulRajput | [Sketchfab](https://sketchfab.com/3d-models/golden-pheasant-f9b3decb485c4a7c9d97cf70b17cbd29) | [CC BY 4.0](http://creativecommons.org/licenses/by/4.0/) |

---

## Appendix: Feature Reference

### Print Settings

| Setting | Default | Description |
|---------|---------|-------------|
| Size (mm) | 100 | Scale the model so its largest extent equals this value in mm |
| Scale | 1.0 | Relative scale multiplier |
| Nozzle diameter | 0.4 mm | Controls voxel cell width. First layer: `nozzle × 1.275`. Upper layers: `nozzle × 1.05`. |
| Layer height | 0.20 mm | Controls voxel cell height |

### Color Adjustments

| Setting | Range | Default | Description |
|---------|-------|---------|-------------|
| Brightness | -100 to +100 | 0 | Shifts all colors lighter or darker before palette selection |
| Contrast | -100 to +100 | 0 | Increases or reduces the tonal range |
| Saturation | -100 to +100 | 0 | Increases or reduces color intensity |

### Stickers

Stickers composite PNG images onto the model surface before voxelization.

| Field | Description |
|-------|-------------|
| Image | PNG file to use as the sticker |
| Placement | Set by clicking a point on the input model. Determines position, surface normal, and orientation. |
| Scale | Size of the sticker on the surface |
| Rotation | Rotation of the sticker around the surface normal |

Multiple stickers can be added. They are applied in order and composited over
the base model color. Sticker colors are subject to the same brightness,
contrast, and saturation adjustments as the rest of the model.

Stickers are saved as part of the JSON settings file.

### Color Pins (Warp Pins)

Each pin maps a source color to a target filament color using Gaussian RBF
interpolation in CIELAB color space.

| Field | Description |
|-------|-------------|
| Source color | Color in the model to remap. Enter as `#RRGGBB` or use the eyedropper to sample from the input viewer. |
| Target color | Filament color to map toward. Selected from a filament collection. |
| Reach (sigma) | Gaussian falloff in delta E units (1–100, default 5). Controls how broadly similar colors are also shifted. |

Up to 8 pins. Invalid pins (missing source or target hex) are silently ignored.

### Palette

| Feature | Description |
|---------|-------------|
| Color slots | 1–16 slots. Each slot is locked (specific filament) or unlocked (auto-selected from collection). |
| Lock / unlock | Click the lock icon on a swatch to toggle. Auto-selected colors are shown with a dashed border. |
| Collection picker | Click a slot swatch to open the filament picker and lock that slot to a color. |
| Unlocked colors from | The filament collection used to fill unlocked slots. |

### Color Snap

Shifts each voxel's color toward its nearest palette color by up to the
configured delta E distance before dithering. Reduces noise in nearly
solid-color regions.

| Setting | Range | Default | Description |
|---------|-------|---------|-------------|
| Color snap | 0–50 delta E | 5 | Pre-dither snap distance. Set to 0 to disable. |

### Filament Collections

| Feature | Description |
|---------|-------------|
| Built-in collection | "Panchroma Basic" — 28 colors, read-only |
| Custom collections | Created via Filaments > New... or Filaments > Import... |
| Import format | Plain text, one color per line: `#RRGGBB Label` |
| Editing | Click a swatch in the collection editor to change its hex value or label |

### Settings Files (JSON)

| Operation | Menu item | Behavior |
|-----------|-----------|---------|
| Save | File > Save JSON | Saves to current path; prompts for path if unsaved |
| Save to new file | File > Save JSON As... | Always prompts for a file path |
| Load | File > Open | Opening a `.json` file restores all settings and re-opens the model |

Saved settings include: input file path, size/scale, nozzle diameter, layer
height, palette (locked colors and collection), color adjustments, color pins,
stickers, dither mode, color snap, and advanced flags.

### Advanced Options (GUI)

These options are in the **Advanced** section of the settings panel (collapsed by default).

| Option | Default | Description |
|--------|---------|-------------|
| Dither mode | `dizzy` | `dizzy`: random traversal with error diffusion, blue-noise-like output. `none`: nearest palette color, no dithering. |
| No merge | off | Disables coplanar triangle merging in the final mesh |
| No simplify | off | Disables QEM mesh decimation before clipping |
| Uniform grid | off | Uses a single voxel size for all layers instead of the wider first-layer grid |
| Stats | off | Logs face counts per material to the status bar |

---

## Known Issues

- Sometimes generates features too thin for the slicer to print.

## Status

Early development. The output 3MF includes embedded printer profiles for the
Snapmaker U1 with a 0.4 mm nozzle. Other printers may need manual profile
adjustment in the slicer.
