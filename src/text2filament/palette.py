"""Parse a filament color palette and assign mesh faces to nearest palette colors."""

import numpy as np
from colorspacious import cspace_convert


def parse_palette(hex_colors: list[str]) -> np.ndarray:
    """Parse a list of hex color strings into an (P, 3) uint8 RGB array."""
    result = []
    for h in hex_colors:
        h = h.strip().lstrip("#")
        if len(h) != 6:
            raise ValueError(f"Invalid hex color: #{h!r} — expected 6 hex digits")
        r = int(h[0:2], 16)
        g = int(h[2:4], 16)
        b = int(h[4:6], 16)
        result.append([r, g, b])
    return np.array(result, dtype=np.uint8)


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
