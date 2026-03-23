"""Sample texture color at the centroid UV of each mesh face."""

import numpy as np
from PIL import Image

from .loader import LoadedModel


def sample_face_colors(model: LoadedModel) -> np.ndarray:
    """
    For each face, compute its centroid UV and sample the texture there.
    Returns (F, 3) uint8 RGB array.
    """
    texture_rgb = np.array(model.texture.convert("RGB"), dtype=np.uint8)  # (H, W, 3)
    H, W = texture_rgb.shape[:2]

    # Average the three vertex UVs per face → (F, 2)
    face_uvs = model.uvs[model.mesh.faces]   # (F, 3, 2)
    centroid_uvs = face_uvs.mean(axis=1)     # (F, 2)

    # Clamp to [0, 1]
    centroid_uvs = np.clip(centroid_uvs, 0.0, 1.0)

    u = centroid_uvs[:, 0]
    v = centroid_uvs[:, 1]

    # Convert to pixel coords. Flip Y: UV (0,0) is bottom-left, image (0,0) is top-left.
    px = u * (W - 1)
    py = (1.0 - v) * (H - 1)

    # Bilinear interpolation
    x0 = np.floor(px).astype(np.int32)
    y0 = np.floor(py).astype(np.int32)
    x1 = np.clip(x0 + 1, 0, W - 1)
    y1 = np.clip(y0 + 1, 0, H - 1)
    x0 = np.clip(x0, 0, W - 1)
    y0 = np.clip(y0, 0, H - 1)

    fx = (px - np.floor(px))[:, np.newaxis]   # (F, 1)
    fy = (py - np.floor(py))[:, np.newaxis]   # (F, 1)

    p00 = texture_rgb[y0, x0].astype(np.float32)  # (F, 3)
    p10 = texture_rgb[y0, x1].astype(np.float32)
    p01 = texture_rgb[y1, x0].astype(np.float32)
    p11 = texture_rgb[y1, x1].astype(np.float32)

    color = (1 - fx) * (1 - fy) * p00 \
          +      fx  * (1 - fy) * p10 \
          + (1 - fx) *      fy  * p01 \
          +      fx  *      fy  * p11

    return np.clip(np.round(color), 0, 255).astype(np.uint8)


def sample_face_indices(model: LoadedModel, palette_rgb: np.ndarray,
                        save_path: "str | None" = None) -> np.ndarray:
    """
    Dither the texture to the palette using Floyd-Steinberg, then sample the
    palette index at each face's centroid UV (nearest-neighbor).
    Returns (F,) int32 array of palette indices.
    """
    texture_rgb = model.texture.convert("RGB")

    # Build a 1-pixel palette image with our colors
    palette_img = Image.new("P", (1, 1))
    flat: list[int] = []
    for r, g, b in palette_rgb:
        flat.extend([int(r), int(g), int(b)])
    flat.extend([0] * (768 - len(flat)))  # Pillow requires 256-entry palette
    palette_img.putpalette(flat)

    # Quantize with Floyd-Steinberg dithering
    dithered = texture_rgb.quantize(palette=palette_img, dither=Image.Dither.FLOYDSTEINBERG)
    if save_path is not None:
        dithered.convert("RGB").save(save_path)
    dithered_array = np.array(dithered, dtype=np.int32)  # (H, W) palette indices

    H, W = dithered_array.shape

    # Sample palette index at face centroid (nearest-neighbor)
    face_uvs = model.uvs[model.mesh.faces]        # (F, 3, 2)
    centroid_uvs = face_uvs.mean(axis=1)          # (F, 2)
    centroid_uvs = np.clip(centroid_uvs, 0.0, 1.0)

    px = np.clip(np.round(centroid_uvs[:, 0] * (W - 1)).astype(np.int32), 0, W - 1)
    py = np.clip(np.round((1.0 - centroid_uvs[:, 1]) * (H - 1)).astype(np.int32), 0, H - 1)

    return dithered_array[py, px]
