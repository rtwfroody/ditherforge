"""Parse a filament color palette and assign mesh faces to nearest palette colors."""

import numpy as np
from colorspacious import cspace_convert
from PIL import Image, ImageColor


def parse_palette(colors: list[str]) -> np.ndarray:
    """
    Parse a list of color strings into an (P, 3) uint8 RGB array.
    Accepts CSS named colors (e.g. 'red', 'darkblue') and hex strings (e.g. '#FF0000').
    """
    result = []
    for c in colors:
        c = c.strip()
        try:
            rgb = ImageColor.getrgb(c)[:3]  # drop alpha if present
        except (ValueError, AttributeError):
            raise ValueError(f"Unknown color {c!r} — use a CSS name (e.g. 'red') or hex (e.g. '#FF0000')")
        result.append(list(rgb))
    return np.array(result, dtype=np.uint8)


def compute_palette(texture: Image.Image, n: int, seed: int = 0) -> np.ndarray:
    """
    Find n dominant colors in the texture using k-means in CIELAB space.
    Returns (n, 3) uint8 RGB array, sorted by CIELAB lightness descending.
    """
    from scipy.cluster.vq import kmeans  # type: ignore[import-untyped]

    pixels = np.array(texture.convert("RGB")).reshape(-1, 3).astype(np.float32)

    # Subsample for speed — 20k pixels is plenty for k-means
    rng = np.random.default_rng(seed)
    if len(pixels) > 20_000:
        pixels = pixels[rng.choice(len(pixels), 20_000, replace=False)]

    pixels_lab = cspace_convert(pixels, "sRGB255", "CIELab").astype(np.float32)

    np.random.seed(seed)  # scipy.cluster.vq uses the global numpy RNG
    centroids_lab, _ = kmeans(pixels_lab, n)

    centroids_rgb = cspace_convert(centroids_lab, "CIELab", "sRGB255")
    centroids_rgb = np.clip(np.round(centroids_rgb), 0, 255).astype(np.uint8)

    # Sort by lightness descending (lightest first — usually the background)
    order = np.argsort(centroids_lab[:, 0])[::-1]
    return centroids_rgb[order]


def assign_palette(
    face_colors_rgb: np.ndarray,  # (F, 3) uint8
    palette_rgb: np.ndarray,      # (P, 3) uint8
    color_space: str = "cielab",
) -> np.ndarray:
    """Assign each face to the nearest palette color. Returns (F,) int array of palette indices."""
    if color_space == "cielab":
        face_lab = cspace_convert(face_colors_rgb.astype(float), "sRGB255", "CIELab")   # (F, 3)
        palette_lab = cspace_convert(palette_rgb.astype(float), "sRGB255", "CIELab")    # (P, 3)
        diffs = face_lab[:, np.newaxis, :] - palette_lab[np.newaxis, :, :]              # (F, P, 3)
    elif color_space == "rgb":
        face_f = face_colors_rgb.astype(float)
        palette_f = palette_rgb.astype(float)
        diffs = face_f[:, np.newaxis, :] - palette_f[np.newaxis, :, :]                  # (F, P, 3)
    else:
        raise ValueError(f"Unknown color space: {color_space!r}")

    distances = np.sqrt((diffs ** 2).sum(axis=2))  # (F, P)
    return distances.argmin(axis=1).astype(np.int32)  # (F,)
