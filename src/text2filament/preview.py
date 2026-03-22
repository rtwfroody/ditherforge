"""Export a colored PLY for visual inspection before committing to 3MF."""

import numpy as np
import trimesh
import trimesh.visual

from .loader import LoadedModel


def export_preview(
    model: LoadedModel,
    assignments: np.ndarray,   # (F,) int — palette index per face
    palette_rgb: np.ndarray,   # (P, 3) uint8
    output_path: str,
) -> None:
    face_colors_rgb = palette_rgb[assignments]                          # (F, 3) uint8
    alpha = np.full((len(face_colors_rgb), 1), 255, dtype=np.uint8)
    face_colors_rgba = np.hstack([face_colors_rgb, alpha])              # (F, 4) uint8

    preview_mesh = trimesh.Trimesh(
        vertices=model.mesh.vertices,
        faces=model.mesh.faces,
        process=False,
    )
    preview_mesh.visual = trimesh.visual.ColorVisuals(
        mesh=preview_mesh,
        face_colors=face_colors_rgba,
    )
    preview_mesh.export(output_path)
