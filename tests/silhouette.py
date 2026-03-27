#!/usr/bin/env python3
"""Render an orthographic silhouette of a GLB or 3MF mesh to PNG.

Usage:
    python3 scripts/silhouette.py <file.glb|file.3mf> [--angle DEG] [--elevation DEG] [-o output.png]

Angle is azimuth in degrees (0 = +X, 90 = +Y, 180 = -X, 270 = -Y).
Elevation is degrees above the horizon (default 0 = side view, 30 = isometric-ish).
"""
import argparse
import math
import sys

import numpy as np
import trimesh
from PIL import Image, ImageDraw


def load_mesh(path: str, normalize: bool = False) -> trimesh.Trimesh:
    """Load a mesh file, merging all geometry into one Trimesh.

    Normalizes to Z-up: GLB/glTF is Y-up so we rotate X by +90°,
    3MF is already Z-up. If normalize=True, also scales to a unit bounding
    box centered at origin (for comparing meshes at different scales).
    """
    scene = trimesh.load(path)
    if isinstance(scene, trimesh.Scene):
        meshes = [g for g in scene.geometry.values() if isinstance(g, trimesh.Trimesh)]
        if not meshes:
            sys.exit(f"No triangle meshes found in {path}")
        mesh = trimesh.util.concatenate(meshes)
    elif isinstance(scene, trimesh.Trimesh):
        mesh = scene
    else:
        sys.exit(f"Unsupported geometry type: {type(scene)}")

    # GLB is Y-up; convert to Z-up by swapping Y and Z (rotate -90° around X).
    if path.lower().endswith((".glb", ".gltf")):
        v = mesh.vertices.copy()
        mesh.vertices = np.column_stack([v[:, 0], -v[:, 2], v[:, 1]])

    if normalize:
        # Center at origin, scale to unit bounding box. This makes meshes
        # comparable regardless of their original units or scale.
        v = mesh.vertices
        lo, hi = v.min(axis=0), v.max(axis=0)
        extent = (hi - lo).max()
        if extent > 0:
            mesh.vertices = (v - (lo + hi) / 2) / extent

    return mesh


def rotation_matrix(azimuth_deg: float, elevation_deg: float) -> np.ndarray:
    """Build a 3x3 rotation: azimuth around Z, then elevation tilting up."""
    az = math.radians(azimuth_deg + 90)
    el = math.radians(elevation_deg)
    # Rotation around Z (vertical in 3MF coords)
    rz = np.array([
        [math.cos(az), -math.sin(az), 0],
        [math.sin(az),  math.cos(az), 0],
        [0,             0,            1],
    ])
    # Rotation around X (tilt up/down)
    rx = np.array([
        [1, 0,             0],
        [0, math.cos(el), -math.sin(el)],
        [0, math.sin(el),  math.cos(el)],
    ])
    return rx @ rz


def projected_bounds(mesh: trimesh.Trimesh, azimuth: float,
                     elevation: float) -> tuple:
    """Compute (x_min, x_max, y_min, y_max) in projected space."""
    rot = rotation_matrix(azimuth, elevation)
    verts = mesh.vertices @ rot.T
    return (verts[:, 0].min(), verts[:, 0].max(),
            verts[:, 2].min(), verts[:, 2].max())


def union_bounds(*bounds_list) -> tuple:
    """Return the union of multiple (x_min, x_max, y_min, y_max) tuples."""
    x_min = min(b[0] for b in bounds_list)
    x_max = max(b[1] for b in bounds_list)
    y_min = min(b[2] for b in bounds_list)
    y_max = max(b[3] for b in bounds_list)
    return (x_min, x_max, y_min, y_max)


def render_silhouette(mesh: trimesh.Trimesh, azimuth: float, elevation: float,
                      resolution: int = 1024, bounds=None) -> Image.Image:
    """Render orthographic silhouette from given angle.

    If bounds is provided as (x_min, x_max, y_min, y_max) in projected space,
    use that for framing instead of auto-fitting to this mesh. This ensures
    multiple renders share the same viewport.
    """
    rot = rotation_matrix(azimuth, elevation)
    verts = mesh.vertices @ rot.T  # (N, 3)

    # Project: use X as horizontal, Z as vertical (screen), Y as depth
    proj_x = verts[:, 0]
    proj_y = verts[:, 2]  # Z is up

    # Compute bounding box in projected space
    margin = 0.05
    if bounds is not None:
        x_min, x_max, y_min, y_max = bounds
    else:
        x_min, x_max = proj_x.min(), proj_x.max()
        y_min, y_max = proj_y.min(), proj_y.max()
    x_range = x_max - x_min
    y_range = y_max - y_min
    if x_range == 0 or y_range == 0:
        sys.exit("Degenerate projection (zero extent)")

    # Uniform scale, fit to resolution with margin
    scale = resolution * (1 - 2 * margin) / max(x_range, y_range)
    cx = resolution / 2 - (x_min + x_max) / 2 * scale
    cy = resolution / 2 + (y_min + y_max) / 2 * scale  # flip Y for image coords

    # Map projected vertices to pixel coords
    px = proj_x * scale + cx
    py = -proj_y * scale + cy

    img = Image.new("L", (resolution, resolution), 255)
    draw = ImageDraw.Draw(img)

    faces = mesh.faces
    for f in faces:
        polygon = [(px[f[0]], py[f[0]]),
                    (px[f[1]], py[f[1]]),
                    (px[f[2]], py[f[2]])]
        draw.polygon(polygon, fill=0)

    return img


def main():
    parser = argparse.ArgumentParser(description="Render mesh silhouette to PNG")
    parser.add_argument("input", help="Input file (GLB or 3MF)")
    parser.add_argument("--angle", type=float, default=0,
                        help="Azimuth angle in degrees (0=+X, 90=+Y, 180=-X)")
    parser.add_argument("--elevation", type=float, default=20,
                        help="Elevation above horizon in degrees (default 20)")
    parser.add_argument("--resolution", type=int, default=1024,
                        help="Output image size in pixels (default 1024)")
    parser.add_argument("-o", "--output", default=None,
                        help="Output PNG path (default: derived from input)")
    args = parser.parse_args()

    if args.output is None:
        base = args.input.rsplit(".", 1)[0]
        args.output = f"{base}_silhouette_{int(args.angle)}deg.png"

    print(f"Loading {args.input}...")
    mesh = load_mesh(args.input)
    print(f"  {len(mesh.vertices)} vertices, {len(mesh.faces)} faces")
    print(f"Rendering silhouette: azimuth={args.angle}°, elevation={args.elevation}°")
    img = render_silhouette(mesh, args.angle, args.elevation, args.resolution)
    img.save(args.output)
    print(f"Saved {args.output}")


if __name__ == "__main__":
    main()
