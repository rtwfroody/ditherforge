#!/usr/bin/env python3
"""Silhouette-based regression tests for ditherforge.

For each test vector (input file + args), runs ditherforge to produce a 3MF,
renders orthographic silhouettes of the input and output from three views
(front, left, top), then checks that the output silhouette approximately
matches the input silhouette.

Comparison: dilate the input silhouette by a margin derived from the hex cell
size and layer height (the maximum discretization error), then assert that all
output-black pixels fall within the dilated input region. Also check that the
output covers a minimum fraction of the input silhouette (no missing geometry).

Usage:
    python3 tests/test_silhouette.py [--keep] [--verbose] [test_name ...]
"""
import argparse
import math
import os
import subprocess
import sys
import tempfile

import numpy as np
from PIL import Image, ImageFilter

from silhouette import load_mesh, render_silhouette

# Each test vector: name, input file, ditherforge extra args, and parameters
# needed to compute the discretization tolerance.
TEST_VECTORS = [
    {
        "name": "boombox",
        "input": "objects/boombox_4k.glb",
        "args": ["--scale", ".13", "--palette", "gray,black,white,red"],
        "nozzle": 0.4,
        "layer_height": 0.2,
        # GLB default unit is meters; extent after scale: ~0.094m * 1000 = 94mm
        "model_extent_mm": 94.1,
    },
    {
        "name": "cake",
        "input": "objects/handpainted_watercolor_cake.glb",
        "args": ["--mode", "hexvoxel", "--glb-unit", "mm"],
        "nozzle": 0.4,
        "layer_height": 0.2,
        "model_extent_mm": 5.0,
    },
]

# Views: (name, azimuth, elevation)
VIEWS = [
    ("front", 90, 20),
    ("left", 0, 20),
    ("top", 0, 90),
]

RESOLUTION = 512
MARGIN = 0.05  # fraction of image used as border margin in render

# Minimum fraction of input silhouette pixels that the output must cover.
MIN_COVERAGE = 0.95
# Maximum fraction of output pixels that may fall outside the dilated input.
# Zero tolerance — the dilation already accounts for hex discretization.
MAX_OVERSHOOT = 0.0


def compute_dilate_px(nozzle_mm, layer_height_mm, model_extent_mm, resolution):
    """Compute dilation radius in pixels from physical cell dimensions.

    The hex cell is hexFlat = nozzle * 1.5 across (flat-to-flat), and one
    layer_height tall. The maximum discretization error is the diagonal of
    the bounding box of one cell. We convert this to pixels via the
    normalized coordinate system used for rendering.
    """
    hex_flat = nozzle_mm * 1.5
    # Hex cell bounding box: hex_flat wide, hex_flat tall (in XY), layer_height in Z.
    # The max error is the 3D diagonal, but for a 2D silhouette projection
    # the worst case is the 2D diagonal of the largest cell face.
    cell_diag = math.sqrt(hex_flat**2 + max(hex_flat, layer_height_mm)**2)
    # In normalized coordinates (model scaled to unit box), the cell diagonal is:
    cell_normalized = cell_diag / model_extent_mm
    # The render uses: scale = resolution * (1 - 2*margin) / 1.0  (unit box)
    pixels_per_unit = resolution * (1 - 2 * MARGIN)
    # Use 1.5× cell diagonal: 1× for discretization error at the boundary,
    # plus 0.5× for concavities smaller than one cell getting filled in.
    dilate_px = int(math.ceil(1.5 * cell_normalized * pixels_per_unit))
    # Add 1px for silhouette centroid alignment rounding.
    dilate_px += 1
    return dilate_px


def run_ditherforge(input_path, extra_args, output_path):
    """Run ditherforge and return True on success."""
    cmd = ["go", "run", ".", input_path, "--output", output_path] + extra_args
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"  FAIL: ditherforge failed:\n{result.stderr}")
        return False
    return True


def silhouette_centroid(img):
    """Compute the centroid of black pixels in a grayscale image."""
    arr = np.array(img)
    mask = arr < 128
    if not mask.any():
        return None
    ys, xs = np.where(mask)
    return (xs.mean(), ys.mean())


def align_silhouette(output_img, input_img):
    """Shift output_img so its silhouette centroid matches the input's.

    Returns a new image with the output shifted. This corrects for small
    alignment differences caused by different coordinate systems/tessellations.
    """
    inp_c = silhouette_centroid(input_img)
    out_c = silhouette_centroid(output_img)
    if inp_c is None or out_c is None:
        return output_img
    dx = int(round(inp_c[0] - out_c[0]))
    dy = int(round(inp_c[1] - out_c[1]))
    if dx == 0 and dy == 0:
        return output_img
    from PIL import ImageChops
    return ImageChops.offset(output_img, dx, dy)


def dilate_mask(mask, radius):
    """Dilate a boolean mask (True = object) by radius pixels."""
    img = Image.fromarray((mask * 255).astype(np.uint8), mode="L")
    size = 2 * radius + 1
    img = img.filter(ImageFilter.MaxFilter(size=size))
    return np.array(img) > 127


def compare_silhouettes(input_img, output_img, view_name, test_name, outdir,
                        dilate_px):
    """Compare input and output silhouettes. Returns (passed, message)."""
    inp = np.array(input_img)
    out = np.array(output_img)

    # Black pixels = object (value < 128)
    inp_mask = inp < 128
    out_mask = out < 128

    inp_count = inp_mask.sum()
    out_count = out_mask.sum()

    if inp_count == 0:
        return True, f"{view_name}: input has no geometry in this view, skipping"

    # Dilate input mask to allow for hex discretization.
    dilated_inp = dilate_mask(inp_mask, dilate_px)

    # Check overshoot: output pixels outside dilated input.
    overshoot = out_mask & ~dilated_inp
    overshoot_count = overshoot.sum()
    overshoot_frac = overshoot_count / out_count if out_count > 0 else 0

    # Check coverage: what fraction of input pixels are also in output.
    covered = inp_mask & out_mask
    coverage = covered.sum() / inp_count if inp_count > 0 else 0

    # Save debug images if there are issues or we're keeping output.
    if outdir:
        debug = np.zeros((*inp.shape, 3), dtype=np.uint8)
        debug[inp_mask & out_mask] = [0, 180, 0]        # green: both
        debug[inp_mask & ~out_mask] = [0, 0, 180]       # blue: input only
        debug[overshoot] = [255, 0, 0]                  # red: overshoot
        debug[~inp_mask & ~out_mask] = [255, 255, 255]  # white: background
        Image.fromarray(debug).save(
            os.path.join(outdir, f"{test_name}_{view_name}_diff.png"))

    passed = True
    messages = []

    if overshoot_frac > MAX_OVERSHOOT:
        passed = False
        messages.append(
            f"overshoot {overshoot_frac:.1%} > {MAX_OVERSHOOT:.1%} "
            f"({overshoot_count} px outside dilated input)")

    if coverage < MIN_COVERAGE:
        passed = False
        messages.append(
            f"coverage {coverage:.1%} < {MIN_COVERAGE:.1%} "
            f"({covered.sum()}/{inp_count} input px covered)")

    status = "PASS" if passed else "FAIL"
    detail = "; ".join(messages) if messages else f"coverage={coverage:.1%}, overshoot={overshoot_frac:.1%}"
    return passed, f"{view_name}: {status} ({dilate_px}px tolerance) — {detail}"


def run_test(vector, outdir, verbose):
    """Run a single test vector. Returns True if all views pass."""
    name = vector["name"]
    input_path = vector["input"]
    extra_args = vector["args"]

    dilate_px = compute_dilate_px(
        vector["nozzle"], vector["layer_height"],
        vector["model_extent_mm"], RESOLUTION)

    print(f"\n{'='*60}")
    print(f"Test: {name}")
    print(f"  Input: {input_path}")
    print(f"  Args: {' '.join(extra_args)}")
    print(f"  Tolerance: {dilate_px}px (nozzle={vector['nozzle']}mm, "
          f"layer={vector['layer_height']}mm, extent={vector['model_extent_mm']}mm)")

    if not os.path.exists(input_path):
        print(f"  SKIP: input file not found")
        return True  # don't fail on missing test data

    # Run ditherforge.
    with tempfile.NamedTemporaryFile(suffix=".3mf", delete=not outdir) as tmp:
        output_path = tmp.name
        if outdir:
            output_path = os.path.join(outdir, f"{name}.3mf")

        print(f"  Running ditherforge...")
        if not run_ditherforge(input_path, extra_args, output_path):
            return False

        # Each mesh is normalized independently (centered on bbox center,
        # scaled to unit extent). Silhouette centroid alignment (below)
        # corrects for coordinate system and tessellation differences.
        print(f"  Loading meshes...")
        input_mesh = load_mesh(input_path, normalize=True)
        output_mesh = load_mesh(output_path, normalize=True)
        if verbose:
            print(f"    Input:  {len(input_mesh.vertices)} verts, {len(input_mesh.faces)} faces")
            print(f"    Output: {len(output_mesh.vertices)} verts, {len(output_mesh.faces)} faces")

        # Render and compare each view.
        all_passed = True
        for view_name, azimuth, elevation in VIEWS:
            input_img = render_silhouette(input_mesh, azimuth, elevation, RESOLUTION)
            output_raw = render_silhouette(output_mesh, azimuth, elevation, RESOLUTION)
            # Align output to input by matching silhouette centroids.
            # This corrects for coordinate system / tessellation differences.
            output_img = align_silhouette(output_raw, input_img)

            if outdir:
                input_img.save(os.path.join(outdir, f"{name}_{view_name}_input.png"))
                output_img.save(os.path.join(outdir, f"{name}_{view_name}_output.png"))

            passed, msg = compare_silhouettes(
                input_img, output_img, view_name, name, outdir, dilate_px)
            print(f"  {msg}")
            if not passed:
                all_passed = False

    return all_passed


def main():
    parser = argparse.ArgumentParser(description="Silhouette regression tests")
    parser.add_argument("tests", nargs="*", help="Run only named tests")
    parser.add_argument("--keep", action="store_true",
                        help="Keep output files in tests/output/")
    parser.add_argument("--verbose", "-v", action="store_true")
    args = parser.parse_args()

    outdir = None
    if args.keep:
        outdir = os.path.join(os.path.dirname(__file__), "output")
        os.makedirs(outdir, exist_ok=True)
        print(f"Saving output to {outdir}")

    vectors = TEST_VECTORS
    if args.tests:
        vectors = [v for v in vectors if v["name"] in args.tests]
        if not vectors:
            sys.exit(f"No matching tests. Available: {[v['name'] for v in TEST_VECTORS]}")

    all_passed = True
    for vector in vectors:
        if not run_test(vector, outdir, args.verbose):
            all_passed = False

    print(f"\n{'='*60}")
    if all_passed:
        print("All tests passed.")
    else:
        print("Some tests FAILED.")
        sys.exit(1)


if __name__ == "__main__":
    main()
