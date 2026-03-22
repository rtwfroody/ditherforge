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

    meshes = [g for g in scene_or_mesh.geometry.values()
              if isinstance(g, trimesh.Trimesh)]
    if not meshes:
        raise ValueError("GLB contains no triangle meshes")

    textures = [_get_texture(m) for m in meshes]

    # All meshes must share the same texture
    unique = {id(t): t for t in textures}
    if len(unique) > 1:
        raise ValueError(
            f"GLB contains {len(unique)} different textures; "
            "only models with a single shared texture are supported."
        )

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
