"""Sample texture color at the centroid UV of each mesh face."""

import numpy as np
from PIL import Image

from .loader import LoadedModel


def _sample_uvs_from_texture(texture_rgb: np.ndarray, uvs: np.ndarray) -> np.ndarray:
    """Bilinear-sample texture_rgb (H,W,3) at each UV in uvs (N,2). Returns (N,3) float32."""
    H, W = texture_rgb.shape[:2]
    u = np.clip(uvs[:, 0], 0.0, 1.0)
    v = np.clip(uvs[:, 1], 0.0, 1.0)
    px = u * (W - 1)
    py = (1.0 - v) * (H - 1)
    x0 = np.floor(px).astype(np.int32)
    y0 = np.floor(py).astype(np.int32)
    x1 = np.clip(x0 + 1, 0, W - 1)
    y1 = np.clip(y0 + 1, 0, H - 1)
    x0 = np.clip(x0, 0, W - 1)
    y0 = np.clip(y0, 0, H - 1)
    fx = (px - np.floor(px))[:, np.newaxis]
    fy = (py - np.floor(py))[:, np.newaxis]
    p00 = texture_rgb[y0, x0].astype(np.float32)
    p10 = texture_rgb[y0, x1].astype(np.float32)
    p01 = texture_rgb[y1, x0].astype(np.float32)
    p11 = texture_rgb[y1, x1].astype(np.float32)
    return (1 - fx) * (1 - fy) * p00 \
         +      fx  * (1 - fy) * p10 \
         + (1 - fx) *      fy  * p01 \
         +      fx  *      fy  * p11


def sample_face_colors(model: LoadedModel) -> np.ndarray:
    """
    For each face, compute its centroid UV and sample the appropriate texture.
    Returns (F, 3) uint8 RGB array.
    """
    # Average the three vertex UVs per face → (F, 2)
    face_uvs = model.uvs[model.mesh.faces]   # (F, 3, 2)
    centroid_uvs = face_uvs.mean(axis=1)     # (F, 2)

    F = len(centroid_uvs)
    colors = np.zeros((F, 3), dtype=np.float32)

    for tex_idx, tex in enumerate(model.textures):
        mask = model.face_texture_idx == tex_idx
        if not mask.any():
            continue
        texture_rgb = np.array(tex.convert("RGB"), dtype=np.uint8)
        colors[mask] = _sample_uvs_from_texture(texture_rgb, centroid_uvs[mask])

    return np.clip(np.round(colors), 0, 255).astype(np.uint8)


def _spread_bits(v: np.ndarray) -> np.ndarray:
    """Spread 16-bit integer bits for Morton code interleaving."""
    v = v.astype(np.uint32) & np.uint32(0xFFFF)
    v = (v | (v << np.uint32(8))) & np.uint32(0x00FF00FF)
    v = (v | (v << np.uint32(4))) & np.uint32(0x0F0F0F0F)
    v = (v | (v << np.uint32(2))) & np.uint32(0x33333333)
    v = (v | (v << np.uint32(1))) & np.uint32(0x55555555)
    return v


def _morton_codes(u: np.ndarray, v: np.ndarray) -> np.ndarray:
    """Z-order (Morton) codes from uint16 u/v arrays."""
    return _spread_bits(u) | (_spread_bits(v) << np.uint32(1))


def sample_face_indices(model: LoadedModel, palette_rgb: np.ndarray,
                        save_path: "str | None" = None) -> np.ndarray:
    """
    Face-level Floyd-Steinberg error diffusion with Morton-code spatial ordering.

    1. Sample the texture color at each face centroid.
    2. Sort faces by the Morton code of their UV centroid (Z-order curve), so
       spatially nearby faces in texture space are adjacent in the sequence.
    3. Walk the sequence: assign each face the nearest palette color, then
       carry the full quantization error forward to the next face.

    Returns (F,) int32 array of palette indices.
    save_path: if given, saves a debug image with each face centroid painted
               in its assigned palette color.
    """
    # Step 1: sample average color per face centroid
    face_colors = sample_face_colors(model).astype(np.float32)  # (F, 3)

    # Step 2: Morton ordering from UV centroids
    face_uvs = model.uvs[model.mesh.faces]                        # (F, 3, 2)
    centroid_uvs = np.clip(face_uvs.mean(axis=1), 0.0, 1.0)      # (F, 2)

    N = np.uint32((1 << 16) - 1)
    u_int = (centroid_uvs[:, 0] * float(N)).astype(np.uint32)
    v_int = (centroid_uvs[:, 1] * float(N)).astype(np.uint32)
    order = np.argsort(_morton_codes(u_int, v_int), kind="stable")

    inv_order = np.empty_like(order)
    inv_order[order] = np.arange(len(order), dtype=order.dtype)

    # Step 3: FS error diffusion in Morton order
    palette_f = palette_rgb.astype(np.float32)       # (P, 3)
    sorted_colors = face_colors[order]                # (F, 3)
    assignments_sorted = np.empty(len(order), dtype=np.int32)
    error = np.zeros(3, dtype=np.float32)

    for i in range(len(sorted_colors)):
        c = np.clip(sorted_colors[i] + error, 0.0, 255.0)
        diffs = palette_f - c                         # (P, 3)
        idx = int(np.argmin((diffs * diffs).sum(axis=1)))
        assignments_sorted[i] = idx
        error = c - palette_f[idx]                    # carry full error to next face

    assignments = assignments_sorted[inv_order]

    if save_path is not None:
        debug_arr = np.array(model.texture.convert("RGB"))
        H, W = debug_arr.shape[:2]
        px = np.clip(np.round(centroid_uvs[:, 0] * (W - 1)).astype(np.int32), 0, W - 1)
        py = np.clip(np.round((1.0 - centroid_uvs[:, 1]) * (H - 1)).astype(np.int32), 0, H - 1)
        debug_arr[py, px] = palette_rgb[assignments]
        Image.fromarray(debug_arr).save(save_path)

    return assignments
