# Color-selection fixtures

Each `<name>.png` in this directory is a multi-view orthographic strip
of one model's surface texture, rendered with a transparent background.
`tests/color_test.go` loads each PNG and feeds its opaque pixels to
`voxel.ResolvePalette` as cell colors — this lets the regression suite
exercise the scorer on real-world distributions without checking large
GLB/STL/texture files into the repo.

The fixture set:

| name             | source model                                    | colorFn                          |
| ---------------- | ----------------------------------------------- | -------------------------------- |
| `delorean`       | DeLorean DMC-12 (BTTF) GLB                      | UV texture per pixel             |
| `golden_pheasant`| Golden pheasant GLB                             | UV texture per pixel             |
| `earth`          | `tests/objects/earth.glb`                       | UV texture per pixel             |
| `bricks_benchy`  | 3DBenchy STL + Polyhaven `Bricks_2k_8b.zip`     | MaterialX triplanar per pixel    |

## Regenerate

The generator lives at `tests/fixturegen/`. From the repo root:

```sh
# All fixtures whose source model is locally available
cd tests && go run ./fixturegen

# A single fixture
cd tests && go run ./fixturegen --only bricks_benchy
```

Cases whose source model paths don't exist on the developer's machine
are skipped with a log line; the existing `.png` is left untouched.

Source paths are hard-coded in `tests/fixturegen/main.go` — update them
there if your local layout differs. Models in
`~/Documents/3d_print/objects/` and the Bricks texture in
`~/Downloads/` match the project author's setup; substitute your own
when regenerating.

## Layout

Each PNG is a horizontal strip of six axis-aligned ortho views (front,
right, back, left, top, bottom) at a uniform world-to-pixel scale. The
model's longest 3D bounding-box dimension fixes the scale at 512
pixels; per-view canvas sizes follow from that, so a square mm of
surface contributes the same pixel count regardless of which view it
appears in. Views are separated by 8 transparent columns. Background
pixels (alpha < 128) are skipped when the test loads the histogram, so
the gap doesn't pollute the cell-color distribution.
