"""Adaptive mesh subdivision with UV interpolation."""

import numpy as np
import trimesh
from trimesh import remesh

from .loader import LoadedModel

MAX_ITER = 12


def subdivide(model: LoadedModel, max_edge_mm: float) -> LoadedModel:
    """
    Subdivide mesh faces until no edge exceeds max_edge_mm.
    UV coordinates are interpolated at each new midpoint vertex.
    Returns a new LoadedModel (texture is passed through unchanged).
    """
    current_verts = np.array(model.mesh.vertices, dtype=np.float64)
    current_faces = np.array(model.mesh.faces, dtype=np.int64)
    current_uvs = np.array(model.uvs, dtype=np.float32)

    done_verts: list[np.ndarray] = []
    done_faces: list[np.ndarray] = []
    done_uvs: list[np.ndarray] = []

    for i in range(MAX_ITER + 1):
        # Per-face max edge length
        edge_vecs = np.diff(
            current_verts[current_faces[:, [0, 1, 2, 0]]],
            axis=1,
        )  # (F, 3, 3)
        edge_lengths = np.sqrt((edge_vecs ** 2).sum(axis=2))  # (F, 3)
        too_long = (edge_lengths > max_edge_mm).any(axis=1)   # (F,)
        face_ok = ~too_long

        # Compact and stash the done faces for this iteration
        ok_face_verts = current_faces[face_ok].flatten()
        unique, inverse = np.unique(ok_face_verts, return_inverse=True)
        done_verts.append(current_verts[unique])
        done_uvs.append(current_uvs[unique])
        done_faces.append(inverse.reshape(-1, 3))

        if not too_long.any():
            break

        if i >= MAX_ITER:
            raise RuntimeError(
                f"Subdivision did not converge after {MAX_ITER} iterations. "
                "Try a larger --resolution value."
            )

        # vertex_attributes kwarg guarantees a 3-tuple return; trimesh's overloads don't reflect this
        current_verts, current_faces, new_attrs = remesh.subdivide(  # type: ignore[misc]
            current_verts,
            current_faces[too_long],
            vertex_attributes={"uv": current_uvs},
        )
        current_uvs = new_attrs["uv"].astype(np.float32)  # type: ignore[index]

    # Concatenate all done batches with offset vertex indices
    final_verts = np.vstack(done_verts)
    final_uvs = np.vstack(done_uvs)

    offsets = np.cumsum([0] + [len(v) for v in done_verts[:-1]])
    final_faces = np.vstack([
        f + offset for f, offset in zip(done_faces, offsets)
    ])

    new_mesh = trimesh.Trimesh(
        vertices=final_verts,
        faces=final_faces,
        process=False,
    )
    return LoadedModel(mesh=new_mesh, uvs=final_uvs, texture=model.texture)
