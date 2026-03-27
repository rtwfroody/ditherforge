# Silhouette Tests

Regression tests that compare the silhouette of ditherforge's output against
the original input model. Catches problems like stray geometry, missing
features, or shape distortion.

## Quick Start

Run all tests:
```
uv run --with trimesh --with pillow --with numpy python3 tests/test_silhouette.py
```

Run a specific test:
```
uv run --with trimesh --with pillow --with numpy python3 tests/test_silhouette.py boombox
```

## Viewing Debug Images

Use `--keep` to save input/output silhouettes and color-coded diff images to
`tests/output/`:
```
uv run --with trimesh --with pillow --with numpy python3 tests/test_silhouette.py --keep
```

This produces three files per view per test:
- `{test}_{view}_input.png` — silhouette of the original model
- `{test}_{view}_output.png` — silhouette of the ditherforge output
- `{test}_{view}_diff.png` — color-coded comparison:
  - **Green** — both input and output agree (good)
  - **Blue** — input only, missing from output (under-coverage)
  - **Red** — output only, extends beyond input (overshoot/artifact)
  - **White** — background

Open the diff images to quickly spot problems. Red areas indicate stray
geometry or protrusions; blue areas indicate missing features.

## Rendering a Single File

The `silhouette.py` script can render any GLB or 3MF to a PNG silhouette:
```
uv run --with trimesh --with pillow --with numpy python3 tests/silhouette.py mymodel.glb --angle 45 -o side.png
```

Options:
- `--angle` — azimuth in degrees (0 = side/+X, 90 = front/+Y, 180 = opposite side/-X)
- `--elevation` — degrees above horizon (default 20, use 90 for top-down)
- `--resolution` — image size in pixels (default 1024)
- `-o` — output path (default: derived from input filename)

## How Comparison Works

Each view renders an orthographic silhouette (black = object, white = empty) of
both the input and output models. The test then:

1. **Dilates** the input silhouette by 16 pixels to allow for hex grid
   discretization.
2. Checks **overshoot**: output pixels outside the dilated input must be < 2%.
3. Checks **coverage**: output must cover > 80% of the input silhouette.

## Adding Tests

Add entries to `TEST_VECTORS` in `test_silhouette.py`:
```python
{
    "name": "mymodel",
    "input": "objects/mymodel.glb",
    "args": ["--scale", ".5", "--palette", "red,blue"],
},
```

The test will be skipped if the input file doesn't exist.
