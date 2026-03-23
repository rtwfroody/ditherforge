"""Adaptive mesh subdivision with UV interpolation."""

import numpy as np
import trimesh
from trimesh import remesh

from .loader import LoadedModel

MAX_ITER = 12


class TooManyVerticesError(Exception):
    pass


def subdivide(model: LoadedModel, max_edge_mm: float,
              max_vertices: int = 1_000_000) -> LoadedModel:
    """
    Subdivide mesh faces until no edge exceeds max_edge_mm.
    UV coordinates and face_texture_idx are interpolated at each new midpoint vertex.
    Raises TooManyVerticesError if the vertex budget would be exceeded.
    Returns a new LoadedModel (textures are passed through unchanged).
    """
    current_verts = np.array(model.mesh.vertices, dtype=np.float64)
    current_faces = np.array(model.mesh.faces, dtype=np.int64)
    current_uvs = np.array(model.uvs, dtype=np.float32)
    # Store face_texture_idx as a float32 per-vertex attribute so trimesh can
    # interpolate it through subdivision.  Since no edge crosses a texture-group
    # boundary, averaging is exact (both endpoints always carry the same value).
    N_verts = len(current_verts)
    face_tex_per_vertex = np.zeros((N_verts, 1), dtype=np.float32)
    face_tex_per_vertex[current_faces.flatten(), 0] = (
        np.repeat(model.face_texture_idx, 3).astype(np.float32)
    )
    current_face_tex = model.face_texture_idx.copy()

    done_verts: list[np.ndarray] = []
    done_faces: list[np.ndarray] = []
    done_uvs: list[np.ndarray] = []
    done_face_tex: list[np.ndarray] = []

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
        done_face_tex.append(current_face_tex[face_ok])

        if not too_long.any():
            break

        if i >= MAX_ITER:
            raise RuntimeError(
                f"Subdivision did not converge after {MAX_ITER} iterations. "
                "Try a larger --resolution value."
            )

        # Estimate vertices after this pass: existing vertices plus ~1.5 new
        # midpoints per face (3 edges per face, most shared between 2 faces).
        done_count = sum(len(v) for v in done_verts)
        estimated_total = done_count + len(current_verts) + 2 * int(too_long.sum())
        if estimated_total > max_vertices:
            raise TooManyVerticesError(
                f"Estimated {estimated_total:,} vertices would exceed budget of "
                f"{max_vertices:,}"
            )

        # vertex_attributes kwarg guarantees a 3-tuple return; trimesh's overloads don't reflect this
        current_verts, current_faces, new_attrs = remesh.subdivide(  # type: ignore[misc]
            current_verts,
            current_faces[too_long],
            vertex_attributes={"uv": current_uvs, "tex_idx": face_tex_per_vertex},
        )
        current_uvs = new_attrs["uv"].astype(np.float32)  # type: ignore[index]
        face_tex_per_vertex = new_attrs["tex_idx"].astype(np.float32)  # type: ignore[index]
        # Recover per-face tex index from first vertex of each face (round to nearest int)
        # face_tex_per_vertex is (N, 1)
        current_face_tex = np.round(
            face_tex_per_vertex[current_faces[:, 0], 0]
        ).astype(np.int32)

    # Concatenate all done batches with offset vertex indices
    final_verts = np.vstack(done_verts)
    final_uvs = np.vstack(done_uvs)
    final_face_tex = np.concatenate(done_face_tex)

    offsets = np.cumsum([0] + [len(v) for v in done_verts[:-1]])
    final_faces = np.vstack([
        f + offset for f, offset in zip(done_faces, offsets)
    ])

    new_mesh = trimesh.Trimesh(
        vertices=final_verts,
        faces=final_faces,
        process=False,
    )

    N_tex = len(model.textures)
    no_texture_mask: "np.ndarray | None" = None
    if model.no_texture_mask is not None:
        no_texture_mask = final_face_tex >= N_tex

    return LoadedModel(
        mesh=new_mesh,
        uvs=final_uvs,
        textures=model.textures,
        face_texture_idx=final_face_tex,
        no_texture_mask=no_texture_mask,
    )
