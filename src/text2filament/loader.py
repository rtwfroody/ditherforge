"""Load a GLB file and extract mesh, per-vertex UVs, and texture image."""

import sys
import trimesh
import trimesh.visual
import numpy as np
from PIL import Image


class LoadedModel:
    def __init__(self, mesh: trimesh.Trimesh, uvs: np.ndarray,
                 textures: "list[Image.Image]",
                 face_texture_idx: np.ndarray,
                 no_texture_mask: "np.ndarray | None" = None):
        # mesh: Trimesh with vertices and faces
        # uvs: (N, 2) float array, one UV per vertex (aligned to mesh.vertices)
        # textures: list of PIL Images, one per texture group
        # face_texture_idx: (F,) int32 — index into textures for each face;
        #                   len(textures) is used as sentinel for texture-less faces
        # no_texture_mask: (F,) bool or None; True = face had no texture, assign palette[0]
        self.mesh = mesh
        self.uvs = uvs
        self.textures = textures
        self.face_texture_idx = face_texture_idx
        self.no_texture_mask = no_texture_mask

    @property
    def texture(self) -> Image.Image:
        """Primary (first) texture, for single-texture backward compatibility."""
        return self.textures[0]


def load_glb(path: str) -> LoadedModel:
    scene_or_mesh = trimesh.load(path, process=False)

    mesh, textures, face_texture_idx, no_texture_mask = _extract_mesh_and_texture(scene_or_mesh)

    if not isinstance(mesh.visual, trimesh.visual.TextureVisuals):
        assert mesh.visual is not None, "Mesh has no visual data"
        mesh.visual = mesh.visual.to_texture()

    if mesh.visual.uv is None:
        raise ValueError(f"Model has no UV coordinates: {path}")

    uvs = np.array(mesh.visual.uv, dtype=np.float32)

    # GLB uses a right-handed Y-up coordinate system; 3D printing slicers expect
    # Z-up.  Rotate +90° around X to convert: (x, y, z) → (x, -z, y).
    verts = mesh.vertices.copy()
    verts[:, 1], verts[:, 2] = -mesh.vertices[:, 2].copy(), mesh.vertices[:, 1].copy()
    mesh = trimesh.Trimesh(vertices=verts, faces=mesh.faces,
                           visual=mesh.visual, process=False)

    return LoadedModel(mesh=mesh, uvs=uvs, textures=textures,
                       face_texture_idx=face_texture_idx, no_texture_mask=no_texture_mask)


def _extract_mesh_and_texture(
    scene_or_mesh,
) -> "tuple[trimesh.Trimesh, list[Image.Image], np.ndarray, np.ndarray | None]":
    if isinstance(scene_or_mesh, trimesh.Trimesh):
        texture = _get_texture(scene_or_mesh)
        face_texture_idx = np.zeros(len(scene_or_mesh.faces), dtype=np.int32)
        return scene_or_mesh, [texture], face_texture_idx, None

    if not isinstance(scene_or_mesh, trimesh.Scene):
        raise ValueError(f"Unexpected trimesh type: {type(scene_or_mesh)}")

    geom_pairs: list[tuple[np.ndarray, trimesh.Trimesh]] = []
    for frame in scene_or_mesh.graph.nodes_geometry:
        transform, geom_name = scene_or_mesh.graph[frame]
        geom = scene_or_mesh.geometry[geom_name]
        if isinstance(geom, trimesh.Trimesh):
            geom_pairs.append((transform, geom))

    if not geom_pairs:
        raise ValueError("GLB contains no triangle meshes")

    # Separate textured and texture-less geometries.
    textured: list[tuple[np.ndarray, trimesh.Trimesh, Image.Image]] = []
    untextured: list[tuple[np.ndarray, trimesh.Trimesh]] = []
    for transform, geom in geom_pairs:
        try:
            tex = _get_texture(geom)
            textured.append((transform, geom, tex))
        except ValueError:
            untextured.append((transform, geom))

    if not textured:
        raise ValueError("GLB contains no textured meshes")
    if untextured:
        print(f"  Warning: {len(untextured)} geometry/ies have no texture; "
              "their faces will use palette index 0.", file=sys.stderr)

    # Build ordered texture list (unique, in order of first appearance).
    seen: dict[int, int] = {}  # id(tex) → index
    tex_list: list[Image.Image] = []
    for _, _, tex in textured:
        if id(tex) not in seen:
            seen[id(tex)] = len(tex_list)
            tex_list.append(tex)

    if len(tex_list) > 1:
        print(f"  {len(tex_list)} textures found; sampling each geometry from its own texture.")

    # N_tex is used as sentinel for texture-less faces in face_texture_idx.
    N_tex = len(tex_list)

    # Build the combined mesh by manually concatenating vertices, faces, and UVs.
    all_verts: list[np.ndarray] = []
    all_faces: list[np.ndarray] = []
    all_uvs: list[np.ndarray] = []
    tex_idx_parts: list[np.ndarray] = []
    offset = 0

    def _append(m: trimesh.Trimesh, tex_idx: int, is_textured: bool) -> None:
        nonlocal offset
        verts = np.array(m.vertices, dtype=np.float64)
        faces = np.array(m.faces, dtype=np.int64) + offset
        vis = m.visual
        if vis is None:
            vis = trimesh.visual.TextureVisuals()
        elif not isinstance(vis, trimesh.visual.TextureVisuals):
            vis = vis.to_texture()
        uvs = (np.array(vis.uv, dtype=np.float32)
               if is_textured and vis.uv is not None
               else np.zeros((len(verts), 2), dtype=np.float32))
        all_verts.append(verts)
        all_faces.append(faces)
        all_uvs.append(uvs)
        # Sentinel N_tex marks texture-less faces so subdivide can track them.
        sentinel = tex_idx if is_textured else N_tex
        tex_idx_parts.append(np.full(len(faces), sentinel, dtype=np.int32))
        offset += len(verts)

    for transform, geom, tex in textured:
        m = geom.copy()
        m.apply_transform(transform)
        _append(m, seen[id(tex)], is_textured=True)

    for transform, geom in untextured:
        m = geom.copy()
        m.apply_transform(transform)
        _append(m, 0, is_textured=False)

    combined_verts = np.concatenate(all_verts, axis=0)
    combined_faces = np.concatenate(all_faces, axis=0)
    combined_uvs = np.concatenate(all_uvs, axis=0)
    face_texture_idx = np.concatenate(tex_idx_parts)

    material = textured[0][1].visual.material  # type: ignore[union-attr]
    visual = trimesh.visual.TextureVisuals(uv=combined_uvs, material=material)
    combined = trimesh.Trimesh(vertices=combined_verts, faces=combined_faces,
                               visual=visual, process=False)

    no_texture_mask: "np.ndarray | None" = None
    if untextured:
        no_texture_mask = face_texture_idx >= N_tex

    return combined, tex_list, face_texture_idx, no_texture_mask


def _get_texture(mesh: trimesh.Trimesh) -> Image.Image:
    mat = getattr(mesh.visual, "material", None)
    if mat is None:
        raise ValueError("Mesh has no material")

    tex = getattr(mat, "baseColorTexture", None)
    if tex is not None:
        return tex

    tex = getattr(mat, "image", None)
    if tex is not None:
        return tex

    raise ValueError("Mesh material has no texture image")
