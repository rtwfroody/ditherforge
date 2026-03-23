"""Load a GLB file and extract mesh, per-vertex UVs, and texture image."""

import trimesh
import trimesh.visual
import numpy as np
from PIL import Image


class LoadedModel:
    def __init__(self, mesh: trimesh.Trimesh, uvs: np.ndarray, texture: Image.Image):
        # mesh: Trimesh with vertices and faces
        # uvs: (N, 2) float array, one UV per vertex (aligned to mesh.vertices)
        # texture: PIL Image
        self.mesh = mesh
        self.uvs = uvs
        self.texture = texture


def load_glb(path: str) -> LoadedModel:
    scene_or_mesh = trimesh.load(path, process=False)

    mesh, texture = _extract_mesh_and_texture(scene_or_mesh)

    if not isinstance(mesh.visual, trimesh.visual.TextureVisuals):
        assert mesh.visual is not None, "Mesh has no visual data"
        mesh.visual = mesh.visual.to_texture()

    if mesh.visual.uv is None:
        raise ValueError(f"Model has no UV coordinates: {path}")

    uvs = np.array(mesh.visual.uv, dtype=np.float32)
    return LoadedModel(mesh=mesh, uvs=uvs, texture=texture)


def _extract_mesh_and_texture(scene_or_mesh) -> tuple[trimesh.Trimesh, Image.Image]:
    if isinstance(scene_or_mesh, trimesh.Trimesh):
        texture = _get_texture(scene_or_mesh)
        return scene_or_mesh, texture

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

    textures = [_get_texture(g) for _, g in geom_pairs]
    unique = {id(t): t for t in textures}
    if len(unique) > 1:
        raise ValueError(
            f"GLB contains {len(unique)} different textures; "
            "only models with a single shared texture are supported."
        )

    # Apply each node's world transform so scene-level scaling is baked in.
    # apply_transform only affects vertex positions; UV coordinates are unaffected.
    meshes = []
    for transform, geom in geom_pairs:
        m = geom.copy()
        m.apply_transform(transform)
        meshes.append(m)

    texture = next(iter(unique.values()))

    if len(meshes) == 1:
        return meshes[0], texture

    # Concatenate all meshes (they share one texture, so UVs remain valid)
    combined = trimesh.util.concatenate(meshes)
    return combined, texture



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
