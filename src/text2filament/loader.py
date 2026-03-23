"""Load a GLB file and extract mesh, per-vertex UVs, and texture image."""

import sys
import trimesh
import trimesh.visual
import numpy as np
from PIL import Image


class LoadedModel:
    def __init__(self, mesh: trimesh.Trimesh, uvs: np.ndarray, texture: Image.Image,
                 no_texture_mask: "np.ndarray | None" = None):
        # mesh: Trimesh with vertices and faces
        # uvs: (N, 2) float array, one UV per vertex (aligned to mesh.vertices)
        # texture: PIL Image
        # no_texture_mask: (F,) bool array; True = face had no texture, assign palette[0]
        self.mesh = mesh
        self.uvs = uvs
        self.texture = texture
        self.no_texture_mask = no_texture_mask


def load_glb(path: str) -> LoadedModel:
    scene_or_mesh = trimesh.load(path, process=False)

    mesh, texture, no_texture_mask = _extract_mesh_and_texture(scene_or_mesh)

    if not isinstance(mesh.visual, trimesh.visual.TextureVisuals):
        assert mesh.visual is not None, "Mesh has no visual data"
        mesh.visual = mesh.visual.to_texture()

    if mesh.visual.uv is None:
        raise ValueError(f"Model has no UV coordinates: {path}")

    uvs = np.array(mesh.visual.uv, dtype=np.float32)
    return LoadedModel(mesh=mesh, uvs=uvs, texture=texture, no_texture_mask=no_texture_mask)


def _extract_mesh_and_texture(scene_or_mesh) -> "tuple[trimesh.Trimesh, Image.Image, np.ndarray | None]":
    if isinstance(scene_or_mesh, trimesh.Trimesh):
        texture = _get_texture(scene_or_mesh)
        return scene_or_mesh, texture, None

    if not isinstance(scene_or_mesh, trimesh.Scene):
        raise ValueError(f"Unexpected trimesh type: {type(scene_or_mesh)}")

    # Build a list of (transform, geometry) pairs using original objects for
    # texture identity comparison (copy() would change id()).
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

    unique = {id(tex): tex for _, _, tex in textured}
    if len(unique) > 1:
        raise ValueError(
            f"GLB contains {len(unique)} different textures; "
            "only models with a single shared texture are supported."
        )
    texture = next(iter(unique.values()))

    # Build combined mesh by manually concatenating vertices, faces, and UVs.
    # This avoids trimesh visual-type compatibility issues when mixing textured
    # and texture-less meshes.
    all_verts: list[np.ndarray] = []
    all_faces: list[np.ndarray] = []
    all_uvs: list[np.ndarray] = []
    mask_parts: list[np.ndarray] = []
    offset = 0

    def _append(m: trimesh.Trimesh, is_textured: bool) -> None:
        nonlocal offset
        verts = np.array(m.vertices, dtype=np.float64)
        faces = np.array(m.faces, dtype=np.int64) + offset
        vis = m.visual
        if vis is None:
            vis = trimesh.visual.TextureVisuals()
        elif not isinstance(vis, trimesh.visual.TextureVisuals):
            vis = vis.to_texture()
        if is_textured and vis.uv is not None:
            uvs = np.array(vis.uv, dtype=np.float32)
        else:
            uvs = np.zeros((len(verts), 2), dtype=np.float32)
        all_verts.append(verts)
        all_faces.append(faces)
        all_uvs.append(uvs)
        mask_parts.append(np.full(len(faces), not is_textured, dtype=bool))
        offset += len(verts)

    for transform, geom, _ in textured:
        m = geom.copy()
        m.apply_transform(transform)
        _append(m, is_textured=True)

    for transform, geom in untextured:
        m = geom.copy()
        m.apply_transform(transform)
        _append(m, is_textured=False)

    combined_verts = np.concatenate(all_verts, axis=0)
    combined_faces = np.concatenate(all_faces, axis=0)
    combined_uvs = np.concatenate(all_uvs, axis=0)
    no_texture_mask = np.concatenate(mask_parts)

    material = textured[0][1].visual.material  # type: ignore[union-attr]
    visual = trimesh.visual.TextureVisuals(uv=combined_uvs, material=material)
    combined = trimesh.Trimesh(vertices=combined_verts, faces=combined_faces, visual=visual,
                               process=False)

    has_untextured = no_texture_mask.any()
    return combined, texture, no_texture_mask if has_untextured else None


def _get_texture(mesh: trimesh.Trimesh) -> Image.Image:
    mat = getattr(mesh.visual, "material", None)
    if mat is None:
        raise ValueError("Mesh has no material")

    # PBRMaterial (GLB standard)
    tex = getattr(mat, "baseColorTexture", None)
    if tex is not None:
        return tex

    # SimpleMaterial fallback
    tex = getattr(mat, "image", None)
    if tex is not None:
        return tex

    raise ValueError("Mesh material has no texture image")
