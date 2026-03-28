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
from PIL import Image


def load_mesh(path: str, normalize: bool = False,
              ref_center=None, ref_extent=None) -> trimesh.Trimesh:
    """Load a mesh file, merging all geometry into one Trimesh.

    Normalizes to Z-up: GLB/glTF is Y-up so we rotate X by +90°,
    3MF is already Z-up. If normalize=True, centers on center of mass and
    scales by bounding box extent. Pass ref_center and ref_extent to use
    a shared reference frame (so two meshes end up aligned).
    """
    scene = trimesh.load(path)
    if isinstance(scene, trimesh.Scene):
        # Apply scene graph transforms so each node ends up in world space.
        meshes = []
        for node_name in scene.graph.nodes_geometry:
            transform, geometry_name = scene.graph[node_name]
            geom = scene.geometry[geometry_name]
            if isinstance(geom, trimesh.Trimesh):
                m = geom.copy()
                m.apply_transform(transform)
                meshes.append(m)
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
        # Center on bbox center and scale to unit bounding box.
        v = mesh.vertices
        lo, hi = v.min(axis=0), v.max(axis=0)
        center = ref_center if ref_center is not None else (lo + hi) / 2
        extent = ref_extent if ref_extent is not None else (hi - lo).max()
        if extent > 0:
            mesh.vertices = (v - center) / extent

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
    """Compute (x_min, x_max, y_min, y_max, depth_min, depth_max) in projected space."""
    rot = rotation_matrix(azimuth, elevation)
    verts = mesh.vertices @ rot.T
    return (verts[:, 0].min(), verts[:, 0].max(),
            verts[:, 2].min(), verts[:, 2].max(),
            verts[:, 1].min(), verts[:, 1].max())


def union_bounds(*bounds_list) -> tuple:
    """Return the union of multiple (x_min, x_max, y_min, y_max, depth_min, depth_max) tuples."""
    x_min = min(b[0] for b in bounds_list)
    x_max = max(b[1] for b in bounds_list)
    y_min = min(b[2] for b in bounds_list)
    y_max = max(b[3] for b in bounds_list)
    depth_min = min(b[4] for b in bounds_list)
    depth_max = max(b[5] for b in bounds_list)
    return (x_min, x_max, y_min, y_max, depth_min, depth_max)


def render_silhouette(mesh: trimesh.Trimesh, azimuth: float, elevation: float,
                      resolution: int = 1024, bounds=None) -> Image.Image:
    """Render orthographic depth image from given angle using z-buffering.

    Returns an RGBA image where alpha=255 marks object pixels and the
    grayscale RGB value encodes depth (nearest=dark, farthest=bright).
    Background pixels are fully transparent (alpha=0).

    If bounds is provided as (x_min, x_max, y_min, y_max, depth_min, depth_max)
    in projected space, use that for framing and depth mapping instead of
    auto-fitting to this mesh. This ensures multiple renders share the same
    viewport and depth scale.
    """
    rot = rotation_matrix(azimuth, elevation)
    verts = mesh.vertices @ rot.T  # (N, 3)

    # Project: use X as horizontal, Z as vertical (screen), Y as depth
    proj_x = verts[:, 0]
    proj_y = verts[:, 2]  # Z is up
    depth = verts[:, 1]   # Y is depth (into screen)

    # Compute bounding box in projected space
    margin = 0.05
    if bounds is not None:
        x_min, x_max, y_min, y_max = bounds[0], bounds[1], bounds[2], bounds[3]
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

    # Depth range for mapping to 0-255.
    if bounds is not None and len(bounds) >= 6:
        depth_min, depth_max = bounds[4], bounds[5]
    else:
        depth_min, depth_max = depth.min(), depth.max()
    depth_range = depth_max - depth_min
    if depth_range < 1e-12:
        depth_range = 1.0

    # Z-buffer rasterization with per-pixel depth interpolation.
    # Two-pass approach: vectorized centroid fill for small faces, then
    # full barycentric rasterization only for faces spanning >2 pixels.
    zbuf = np.full((resolution, resolution), np.inf, dtype=np.float64)
    depth_img = np.zeros((resolution, resolution), dtype=np.float64)

    faces = mesh.faces
    # Gather per-face vertex data.
    fx = px[faces]  # (N, 3)
    fy = py[faces]  # (N, 3)
    fd = depth[faces]  # (N, 3)

    # Compute face bounding boxes in pixel coords.
    fxmin = np.floor(fx.min(axis=1)).astype(int)
    fxmax = np.ceil(fx.max(axis=1)).astype(int)
    fymin = np.floor(fy.min(axis=1)).astype(int)
    fymax = np.ceil(fy.max(axis=1)).astype(int)

    # Classify: small faces (bbox ≤ 2px in both dimensions) vs large.
    bbox_w = fxmax - fxmin
    bbox_h = fymax - fymin
    small = (bbox_w <= 2) & (bbox_h <= 2)

    # --- Fast path: small faces rendered at centroid (fully vectorized) ---
    small_idx = np.where(small)[0]
    if len(small_idx) > 0:
        cent_x = fx[small_idx].mean(axis=1)
        cent_y = fy[small_idx].mean(axis=1)
        cent_d = fd[small_idx].mean(axis=1)
        pi_x = np.clip(cent_x.astype(int), 0, resolution - 1)
        pi_y = np.clip(cent_y.astype(int), 0, resolution - 1)

        # Sort farthest-first so that when duplicate pixels exist, the
        # nearest face (written last) wins via numpy fancy indexing.
        order = np.argsort(-cent_d)
        pi_y = pi_y[order]
        pi_x = pi_x[order]
        cent_d = cent_d[order]
        zbuf[pi_y, pi_x] = cent_d
        depth_img[pi_y, pi_x] = cent_d

    # --- Full rasterization for large faces ---
    large_idx = np.where(~small)[0]
    for fi in large_idx:
        x0, y0, d0 = float(fx[fi, 0]), float(fy[fi, 0]), float(fd[fi, 0])
        x1, y1, d1 = float(fx[fi, 1]), float(fy[fi, 1]), float(fd[fi, 1])
        x2, y2, d2 = float(fx[fi, 2]), float(fy[fi, 2]), float(fd[fi, 2])

        bx0 = max(0, int(fxmin[fi]))
        by0 = max(0, int(fymin[fi]))
        bx1 = min(resolution - 1, int(fxmax[fi]))
        by1 = min(resolution - 1, int(fymax[fi]))
        if bx0 > bx1 or by0 > by1:
            continue

        v0x, v0y = x1 - x0, y1 - y0
        v1x, v1y = x2 - x0, y2 - y0
        dot00 = v0x * v0x + v0y * v0y
        dot01 = v0x * v1x + v0y * v1y
        dot11 = v1x * v1x + v1y * v1y
        denom = dot00 * dot11 - dot01 * dot01
        if abs(denom) < 1e-10:
            continue
        inv_denom = 1.0 / denom

        pxs = np.arange(bx0, bx1 + 1) + 0.5
        pys = np.arange(by0, by1 + 1) + 0.5
        gx, gy = np.meshgrid(pxs, pys)

        v2x = gx - x0
        v2y = gy - y0
        dot02 = v0x * v2x + v0y * v2y
        dot12 = v1x * v2x + v1y * v2y

        u = (dot11 * dot02 - dot01 * dot12) * inv_denom
        v = (dot00 * dot12 - dot01 * dot02) * inv_denom

        inside = (u >= 0) & (v >= 0) & (u + v <= 1)
        if not inside.any():
            continue

        interp_depth = d0 + u * (d1 - d0) + v * (d2 - d0)

        iy = np.arange(by0, by1 + 1)
        ix = np.arange(bx0, bx1 + 1)
        closer = inside & (interp_depth < zbuf[by0:by1+1, bx0:bx1+1])
        if closer.any():
            rows, cols = np.where(closer)
            zbuf[iy[rows], ix[cols]] = interp_depth[closer]
            depth_img[iy[rows], ix[cols]] = interp_depth[closer]

    # Convert z-buffer to RGBA image.
    has_geom = zbuf < np.inf
    gray = np.zeros((resolution, resolution), dtype=np.uint8)
    if has_geom.any():
        normalized = (depth_img[has_geom] - depth_min) / depth_range
        gray[has_geom] = np.clip(normalized * 255, 0, 255).astype(np.uint8)

    rgba = np.zeros((resolution, resolution, 4), dtype=np.uint8)
    rgba[:, :, 0] = gray
    rgba[:, :, 1] = gray
    rgba[:, :, 2] = gray
    rgba[has_geom, 3] = 255

    return Image.fromarray(rgba)


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
