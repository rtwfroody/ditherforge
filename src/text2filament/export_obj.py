"""Export a mesh with per-face material assignments as OBJ + MTL."""

import os
import numpy as np

from .loader import LoadedModel


def export_obj(
    model: LoadedModel,
    assignments: np.ndarray,   # (F,) int — palette index per face
    palette_hex: list[str],    # ["#RRGGBB", ...]
    output_path: str,
) -> None:
    base = os.path.splitext(output_path)[0]
    mtl_path = base + ".mtl"
    mtl_name = os.path.basename(mtl_path)

    _write_mtl(mtl_path, palette_hex)
    _write_obj(output_path, mtl_name, model, assignments)


def _write_mtl(path: str, palette_hex: list[str]) -> None:
    lines: list[str] = []
    for i, h in enumerate(palette_hex):
        h = h.strip().lstrip("#")
        r = int(h[0:2], 16) / 255
        g = int(h[2:4], 16) / 255
        b = int(h[4:6], 16) / 255
        lines.append(f"newmtl mat_{i}")
        lines.append(f"Kd {r:.6f} {g:.6f} {b:.6f}")
        lines.append("Ka 0 0 0")
        lines.append("Ks 0 0 0")
        lines.append("d 1.0")
        lines.append("")
    with open(path, "w") as f:
        f.write("\n".join(lines))


def _write_obj(
    path: str,
    mtl_name: str,
    model: LoadedModel,
    assignments: np.ndarray,
) -> None:
    vertices = model.mesh.vertices
    faces = model.mesh.faces

    # Sort faces by material so we can emit contiguous usemtl blocks
    order = np.argsort(assignments, kind="stable")
    sorted_faces = faces[order]
    sorted_assignments = assignments[order]

    lines: list[str] = []
    lines.append(f"mtllib {mtl_name}")
    lines.append("")

    for x, y, z in vertices:
        lines.append(f"v {x:.6f} {y:.6f} {z:.6f}")
    lines.append("")

    current_mat = -1
    for (v1, v2, v3), mat in zip(sorted_faces, sorted_assignments):
        if mat != current_mat:
            lines.append(f"usemtl mat_{mat}")
            current_mat = mat
        # OBJ uses 1-based vertex indices
        lines.append(f"f {v1+1} {v2+1} {v3+1}")

    with open(path, "w") as f:
        f.write("\n".join(lines))
