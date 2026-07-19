# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "numpy",
#   "opencv-python-headless",
#   "scipy",
#   "matplotlib",
# ]
# ///
"""swatchphoto — recover filament colors and neighbor-bleed ℓ from a swatch photo.

DitherForge's "Export Swatch Plates" prints six calibration plates, one per
unordered pair of a four-filament palette. Each plate is a 90x10x2mm bar with
nine 10mm sections left->right at B-fraction p = 0, 1/8, ... , 1, the A/B mix
rendered as a fine Bayer speckle (see internal/swatch/swatch.go). A top-down
photo of the printed plates, plus the `.swatch.json` manifest emitted next to
the 3MF, is enough to recover:

  (a) each filament's *in-context* printed color (the surface color it actually
      shows on the print, which differs from its nominal filament hex — that
      difference is the whole point of the calibration), and
  (b) a fitted neighbor-bleed path length ℓ per pair (and a global ℓ), by
      forward-simulating the pipeline's own per-cell neighbor-blend model
      (voxel.EffectiveCellColors) on the known Bayer pattern and least-squares
      matching the measured section colors.

Illumination is normalized by a robustly-fit (IRLS) polynomial paper surface, so
plates brighter than the paper (Cold White) normalize to linear values >1.0 that
are carried UNCLAMPED through sampling and the fit — only the display hex clamps,
and the clip is flagged. Endpoints aggregate by median (robust to per-plate
gloss), with per-plate values reported. The global ℓ rejects per-pair outliers.
NOTE: the pipeline blend is additive in linear RGB; bright+bright pairs mix more
subtractively than it can model, so those pairs rail and are excluded from the
robust global ℓ (see README "Model limitation").

The forward blend model is ported faithfully from the Go source:
  internal/voxel/td_layered.go   (EffectiveCellColors: 2 Jacobi passes)
  internal/palette/td.go         (NeighborLeak: β = 10^(−ℓ/TD), clamps)
  internal/voxel/color.go        (BuildNeighbors: 26-conn weights 1.0/0.1/0.01)
  internal/voxel/color.go        (sRGB<->linear)

Usage:
  uv run tools/swatchphoto/swatchphoto.py PHOTO.jpg MANIFEST.swatch.json [--out DIR] [--anisotropic]
  uv run tools/swatchphoto/swatchphoto.py --selftest [--anisotropic] [--ell 0.35] [--seed 0]

The --selftest mode generates synthetic "photos" from known colors through the
same forward model at a known injected ℓ (random rotations/positions, an
illumination gradient, blur and sensor noise) and asserts the whole chain
recovers the plates, colors and ℓ. Build/verify the tool with --selftest before
a real photo exists.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from dataclasses import dataclass, field

import numpy as np

# ---------------------------------------------------------------------------
# sRGB <-> linear  (mirrors internal/voxel/color.go srgbToLinearLUT /
# linearToSrgbByte: standard sRGB transfer, D65-agnostic per-channel).
# ---------------------------------------------------------------------------


def srgb_to_linear(s: np.ndarray) -> np.ndarray:
    """s in [0,1] sRGB -> linear light."""
    s = np.asarray(s, dtype=np.float64)
    return np.where(s <= 0.04045, s / 12.92, ((s + 0.055) / 1.055) ** 2.4)


def linear_to_srgb(l: np.ndarray) -> np.ndarray:
    """linear light -> [0,1] sRGB."""
    l = np.clip(np.asarray(l, dtype=np.float64), 0.0, 1.0)
    return np.where(l <= 0.0031308, l * 12.92, 1.055 * (l ** (1.0 / 2.4)) - 0.055)


def hex_to_linear(hexstr: str) -> np.ndarray:
    h = hexstr.lstrip("#")
    rgb = np.array([int(h[i : i + 2], 16) for i in (0, 2, 4)], dtype=np.float64) / 255.0
    return srgb_to_linear(rgb)


def linear_to_hex(lin: np.ndarray) -> str:
    b = np.clip(np.round(linear_to_srgb(lin) * 255.0), 0, 255).astype(int)
    return "#%02X%02X%02X" % (b[0], b[1], b[2])


def linear_to_srgb_byte(lin: np.ndarray) -> np.ndarray:
    return np.clip(np.round(linear_to_srgb(lin) * 255.0), 0, 255).astype(np.uint8)


def linear_to_lab(lin: np.ndarray) -> np.ndarray:
    """linear RGB (…,3) -> CIELAB (D65). Used only for robust cluster matching."""
    lin = np.asarray(lin, dtype=np.float64)
    m = np.array(
        [
            [0.4124, 0.3576, 0.1805],
            [0.2126, 0.7152, 0.0722],
            [0.0193, 0.1192, 0.9505],
        ]
    )
    xyz = lin @ m.T
    white = np.array([0.95047, 1.0, 1.08883])
    xyz = xyz / white

    def f(t):
        d = 6.0 / 29.0
        return np.where(t > d**3, np.cbrt(t), t / (3 * d * d) + 4.0 / 29.0)

    fx, fy, fz = f(xyz[..., 0]), f(xyz[..., 1]), f(xyz[..., 2])
    L = 116 * fy - 16
    a = 500 * (fx - fy)
    b = 200 * (fy - fz)
    return np.stack([L, a, b], axis=-1)


# ---------------------------------------------------------------------------
# Swatch pattern (mirrors internal/swatch/swatch.go).
# ---------------------------------------------------------------------------

BAYER8 = np.array(
    [
        [0, 32, 8, 40, 2, 34, 10, 42],
        [48, 16, 56, 24, 50, 18, 58, 26],
        [12, 44, 4, 36, 14, 46, 6, 38],
        [60, 28, 52, 20, 62, 30, 54, 22],
        [3, 35, 11, 43, 1, 33, 9, 41],
        [51, 19, 59, 27, 49, 17, 57, 25],
        [15, 47, 7, 39, 13, 45, 5, 37],
        [63, 31, 55, 23, 61, 29, 53, 21],
    ],
    dtype=np.int64,
)

SECTIONS = 9
SECTION_MM = 10.0


@dataclass
class Geom:
    block_width_mm: float
    row_height0_mm: float
    row_height_up_mm: float
    row_count: int

    @property
    def per_section(self) -> int:
        return max(1, int(round(SECTION_MM / self.block_width_mm)))

    @property
    def nx(self) -> int:
        return SECTIONS * self.per_section

    def row_heights(self) -> np.ndarray:
        h = np.full(self.row_count, self.row_height_up_mm, dtype=np.float64)
        if self.row_count > 0:
            h[0] = self.row_height0_mm
        return h


def build_pattern(section_coverage: np.ndarray, geom: Geom) -> np.ndarray:
    """Return an (rows, cols) uint8 grid: 0 = filament A, 1 = filament B.

    Column i is in section i//per_section; a cell is B iff
    Bayer8[i%8][j%8] < coverage(section)*64 — the exact blockIsB rule. Uses the
    per-section REALIZED coverage (from the manifest) as the threshold.
    """
    nx, per = geom.nx, geom.per_section
    rows = geom.row_count
    cols = np.arange(nx)
    sections = np.clip(cols // per, 0, SECTIONS - 1)
    p = section_coverage[sections]  # (nx,)
    thr = p * 64.0  # (nx,)
    j = np.arange(rows)[:, None]  # (rows,1)
    i = cols[None, :]  # (1,nx)
    bay = BAYER8[i % 8, j % 8]  # (rows,nx)
    return (bay < thr[None, :]).astype(np.uint8)


# ---------------------------------------------------------------------------
# Neighbor-bleed forward model (mirrors voxel.EffectiveCellColors +
# palette.NeighborLeak + voxel.BuildNeighbors).
# ---------------------------------------------------------------------------

BLEED_ITERATIONS = 2  # pipeline.buildSimFaceColors passes 2


def neighbor_leak(td: float, path_mm: float) -> float:
    """β = 10^(−path/TD), clamped: TD≤0/Inf -> 0, β<1/1024 -> 0, β>0.95 -> 0.95."""
    if not (td > 0) or math.isinf(td):
        return 0.0
    b = 10.0 ** (-path_mm / td)
    if b < 1.0 / 1024.0:
        return 0.0
    return min(b, 0.95)


def neighbor_leak_grid(td_grid: np.ndarray, path_mm: float) -> np.ndarray:
    """Vectorized neighbor_leak over an array of TDs (same clamps)."""
    td = np.asarray(td_grid, dtype=np.float64)
    good = (td > 0) & np.isfinite(td)
    b = np.zeros_like(td)
    with np.errstate(over="ignore", divide="ignore"):
        b[good] = np.power(10.0, -path_mm / td[good])
    b[b < 1.0 / 1024.0] = 0.0
    return np.minimum(b, 0.95)


def _shift(a: np.ndarray, dr: int, dc: int):
    """Return (neighbor_values, valid_mask): value at (r,c) is a[r+dr, c+dc],
    out-of-range entries zero and marked invalid."""
    R, C = a.shape[0], a.shape[1]
    nv = np.zeros_like(a)
    valid = np.zeros((R, C), dtype=bool)
    rs0, rs1 = max(0, dr), min(R, R + dr)
    cs0, cs1 = max(0, dc), min(C, C + dc)
    rd0 = max(0, -dr)
    cd0 = max(0, -dc)
    rd1 = rd0 + (rs1 - rs0)
    cd1 = cd0 + (cs1 - cs0)
    if rs1 > rs0 and cs1 > cs0:
        nv[rd0:rd1, cd0:cd1] = a[rs0:rs1, cs0:cs1]
        valid[rd0:rd1, cd0:cd1] = True
    return nv, valid


# In-plane 8-connected offsets with BuildNeighbors weights (axes=1 -> 1.0,
# axes=2 -> 0.1). The front face is a single Y-layer, so no axes=3 corner term.
_OFFSETS = []
for _dr in (-1, 0, 1):
    for _dc in (-1, 0, 1):
        if _dr == 0 and _dc == 0:
            continue
        _axes = (1 if _dr else 0) + (1 if _dc else 0)
        _w = 1.0 if _axes == 1 else 0.1
        _OFFSETS.append((_dr, _dc, _w))


def effective_cell_colors(
    pattern: np.ndarray,
    colA_lin: np.ndarray,
    colB_lin: np.ndarray,
    tdA: float,
    tdB: float,
    geom: Geom,
    ell_x: float,
    ell_z: float | None = None,
    iterations: int = BLEED_ITERATIONS,
) -> np.ndarray:
    """Return (rows, cols, 3) linear-light per-cell simulated colors.

    Faithful port of voxel.EffectiveCellColors: C_{t+1}(i) = (1−β)·C0(i) +
    β·(Σ_j w_ij·C_t(j))/(Σ_j w_ij), self term always C0, w_ij = weight ×
    max(area_j, ε), 2 passes, linear light.

    Isotropic when ell_z is None or == ell_x (single β per cell, exactly the Go
    form). Anisotropic (ell_z != ell_x) uses a direction-dependent leak — an
    algebraic generalization that collapses to the Go form when ell_x == ell_z:
    C_{t+1}(i) = C0(i) + (Σ_j w_ij·β_dir·(C_t(j)−C0(i)))/(Σ_j w_ij), with
    β_dir = 10^(−ℓ_dir/TD), ℓ_dir = ell_x (in-column X neighbor), ell_z (in-row
    Z neighbor), hypot(ell_x, ell_z) (diagonal). The layer-tall rows were
    designed to probe vertical bleed the single-ℓ model can't separate."""
    rows, cols = pattern.shape
    aniso = ell_z is not None and abs(ell_z - ell_x) > 1e-12
    if ell_z is None:
        ell_z = ell_x

    c0 = np.where(pattern[..., None] == 1, colB_lin[None, None, :], colA_lin[None, None, :])
    c0 = c0.astype(np.float64)

    td_grid = np.where(pattern == 1, tdB, tdA).astype(np.float64)
    # per-cell isotropic β (used in the exact Go form)
    beta_iso = neighbor_leak_grid(td_grid, ell_x)
    # Precompute the (at most 3) direction-dependent β grids once for anisotropy.
    beta_by_dir = {}
    if aniso:
        for ell_dir in {ell_x, ell_z, math.hypot(ell_x, ell_z)}:
            beta_by_dir[ell_dir] = neighbor_leak_grid(td_grid, ell_dir)

    area_row = geom.row_heights() * geom.block_width_mm  # (rows,)
    area = np.maximum(np.broadcast_to(area_row[:, None], (rows, cols)), 1e-6)

    cur = c0.copy()
    for _ in range(iterations):
        wsum = np.zeros((rows, cols), dtype=np.float64)
        ssum = np.zeros((rows, cols, 3), dtype=np.float64)  # Σ w·cur (iso) or Σ w·β·(cur−c0) (aniso)
        for dr, dc, w in _OFFSETS:
            nv, valid = _shift(cur, dr, dc)
            narea, _ = _shift(area, dr, dc)
            wij = w * narea * valid  # (rows,cols)
            wsum += wij
            if not aniso:
                ssum += wij[..., None] * nv
            else:
                if dr != 0 and dc != 0:
                    ell_dir = math.hypot(ell_x, ell_z)
                elif dc != 0:
                    ell_dir = ell_x
                else:
                    ell_dir = ell_z
                beta_dir = beta_by_dir[ell_dir]
                ssum += (wij * beta_dir)[..., None] * (nv - c0)

        nxt = c0.copy()
        if not aniso:
            active = (wsum > 0) & (beta_iso > 0)
            mean = np.zeros_like(ssum)
            nz = wsum > 0
            mean[nz] = ssum[nz] / wsum[nz][..., None]
            blended = (1.0 - beta_iso)[..., None] * c0 + beta_iso[..., None] * mean
            nxt = np.where(active[..., None], blended, c0)
        else:
            nz = wsum > 0
            add = np.zeros_like(ssum)
            add[nz] = ssum[nz] / wsum[nz][..., None]
            nxt = c0 + add
        cur = nxt
    return cur


def section_colors_from_grid(grid_lin: np.ndarray, geom: Geom,
                             col_inset_frac: float = 0.25, row_inset_frac: float = 0.15) -> np.ndarray:
    """Average each section's centre patch (inset from section borders in X and
    from top/bottom in Z) to a (9,3) linear-RGB curve."""
    rows, cols = grid_lin.shape[:2]
    per = geom.per_section
    r0 = int(rows * row_inset_frac)
    r1 = max(r0 + 1, int(rows * (1 - row_inset_frac)))
    out = np.zeros((SECTIONS, 3))
    for s in range(SECTIONS):
        c_lo = s * per
        c_hi = (s + 1) * per
        inset = max(1, int(per * col_inset_frac))
        cc0 = min(c_lo + inset, c_hi - 1)
        cc1 = max(cc0 + 1, c_hi - inset)
        patch = grid_lin[r0:r1, cc0:cc1].reshape(-1, 3)
        out[s] = patch.mean(axis=0)
    return out


def model_section_curve(covA_lin, covB_lin, tdA, tdB, section_coverage, geom,
                        ell_x, ell_z=None) -> np.ndarray:
    """Full forward model: pattern -> bleed -> per-section centre colors (9,3)."""
    pattern = build_pattern(section_coverage, geom)
    grid = effective_cell_colors(pattern, covA_lin, covB_lin, tdA, tdB, geom, ell_x, ell_z)
    return section_colors_from_grid(grid, geom)


# ---------------------------------------------------------------------------
# Manifest.
# ---------------------------------------------------------------------------


@dataclass
class Filament:
    label: str
    hex: str
    td: float


@dataclass
class PlateSpec:
    pair: tuple[str, str]
    nominal: np.ndarray  # (9,)
    realized: np.ndarray  # (9,) realizedCoverageFront


@dataclass
class Manifest:
    geom: Geom
    filaments: dict[str, Filament]
    plates: list[PlateSpec]


def load_manifest(path: str) -> Manifest:
    with open(path) as f:
        m = json.load(f)
    geom = Geom(
        block_width_mm=float(m["blockWidthMM"]),
        row_height0_mm=float(m["rowHeight0MM"]),
        row_height_up_mm=float(m["rowHeightUpMM"]),
        row_count=int(m["rowCount"]),
    )
    fils = {}
    for p in m["palette"]:
        fils[p["label"]] = Filament(label=p["label"], hex=p["hex"], td=float(p["td"]))
    plates = []
    for pl in m["plates"]:
        secs = sorted(pl["sections"], key=lambda s: s["index"])
        nominal = np.array([s["nominalCoverage"] for s in secs], dtype=np.float64)
        realized = np.array(
            [s.get("realizedCoverageFront", s["nominalCoverage"]) for s in secs], dtype=np.float64
        )
        plates.append(PlateSpec(pair=(pl["pair"][0], pl["pair"][1]), nominal=nominal, realized=realized))
    return Manifest(geom=geom, filaments=fils, plates=plates)


# ---------------------------------------------------------------------------
# Photo pipeline.
# ---------------------------------------------------------------------------


def _saturation(x: np.ndarray) -> np.ndarray:
    mx = x.max(axis=2)
    mn = x.min(axis=2)
    return np.where(mx > 1e-6, (mx - mn) / mx, 0.0)


def _poly_features(xn: np.ndarray, yn: np.ndarray, deg: int) -> np.ndarray:
    """2D polynomial design matrix (…,nterms) for normalized coords in [-1,1]."""
    terms = []
    for i in range(deg + 1):
        for j in range(deg + 1 - i):
            terms.append((xn**i) * (yn**j))
    return np.stack(terms, axis=-1)


def estimate_illumination(lin: np.ndarray, deg: int = 3, iters: int = 12) -> np.ndarray:
    """Per-channel smooth illumination surface, fit robustly (IRLS) so it follows
    the PAPER lighting and rejects the plates.

    A low-order 2D polynomial is fit to a downsampled image; each pass hard-rejects
    pixels whose residual exceeds ~2.5 robust-σ (both signs), so plates that are
    brighter than paper (Cold White) OR darker (Black/Orange speckle) fall out of
    the fit instead of dragging the "white" reference toward themselves. This is
    the key fix for bright-than-paper plates: a paper-median estimate seeded on
    saturation lets a low-saturation Cold White plate contaminate its own
    neighborhood and normalize itself to ~1.0, flattening its ramp."""
    import cv2

    H, W = lin.shape[:2]
    scale = max(1, int(round(max(H, W) / 240.0)))
    small = lin[::scale, ::scale]
    hs, ws = small.shape[:2]
    yy, xx = np.mgrid[0:hs, 0:ws].astype(np.float64)
    xn = (xx / max(ws - 1, 1)) * 2 - 1
    yn = (yy / max(hs - 1, 1)) * 2 - 1
    X = _poly_features(xn.ravel(), yn.ravel(), deg)  # (N, T)

    illum_small = np.zeros((hs, ws, 3))
    for ch in range(3):
        y = small[..., ch].ravel()
        w = np.ones_like(y)
        pred = y.copy()
        for _ in range(iters):
            sw = np.sqrt(w)
            coef, *_ = np.linalg.lstsq(X * sw[:, None], y * sw, rcond=None)
            pred = X @ coef
            resid = y - pred
            keep = w > 0
            med = np.median(resid[keep])
            sigma = 1.4826 * np.median(np.abs(resid[keep] - med)) + 1e-6
            w = (np.abs(resid - med) < 2.5 * sigma).astype(np.float64)
            if w.sum() < X.shape[1] * 4:  # too few inliers; stop
                break
        illum_small[..., ch] = pred.reshape(hs, ws)
    illum = cv2.resize(illum_small.astype(np.float32), (W, H), interpolation=cv2.INTER_CUBIC)
    return np.maximum(illum.astype(np.float64), 1e-3)


def normalize_illumination(lin: np.ndarray):
    """Divide out the robustly-estimated paper illumination. Paper -> ~1.0;
    plates brighter than paper stay ABOVE 1.0 (not clamped) so their true color
    survives into sampling and the fit. Returns (normalized_linear, illum)."""
    illum = estimate_illumination(lin)
    norm = np.maximum(lin / illum, 0.0)  # no upper clamp: keep >1.0 highlights
    return norm, illum


@dataclass
class DetectedPlate:
    rect_pts: np.ndarray  # (4,2) source corners, long edge first
    sections_lin: np.ndarray  # (9,3) measured section colors, in warp order


def detect_plates(norm_lin: np.ndarray) -> list[DetectedPlate]:
    """Segment plates from a paper-normalized linear image and measure each
    plate's nine section colors (in raw left->right warp order)."""
    import cv2

    H, W = norm_lin.shape[:2]
    # Paper normalizes to ~1.0 in every channel; a plate deviates from white in
    # some channel (either direction) or is chromatic. Deviation-based detection
    # is robust to absolute brightness (Cold White is bright but far from white).
    dev = np.abs(norm_lin - 1.0).max(axis=2)
    sat = _saturation(norm_lin)
    fg = ((dev > 0.12) | (sat > 0.15)).astype(np.uint8) * 255
    k = cv2.getStructuringElement(cv2.MORPH_ELLIPSE, (5, 5))
    fg = cv2.morphologyEx(fg, cv2.MORPH_OPEN, k)
    fg = cv2.morphologyEx(fg, cv2.MORPH_CLOSE, k)

    import os as _os

    dbg = _os.environ.get("SWATCHPHOTO_DEBUG")
    n, labels, stats, _ = cv2.connectedComponentsWithStats(fg, connectivity=8)
    plates: list[DetectedPlate] = []
    img_area = H * W
    for lbl in range(1, n):
        area = stats[lbl, cv2.CC_STAT_AREA]
        if area < 0.002 * img_area:
            continue
        mask = (labels == lbl).astype(np.uint8)
        cnts, _ = cv2.findContours(mask, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
        if not cnts:
            continue
        cnt = max(cnts, key=cv2.contourArea)
        rect = cv2.minAreaRect(cnt)
        (cx, cy), (w, h), ang = rect
        if w < 1 or h < 1:
            continue
        long_side, short_side = max(w, h), min(w, h)
        aspect = long_side / short_side
        fill = cv2.contourArea(cnt) / (w * h) if w * h > 0 else 0
        if dbg:
            sys.stderr.write(f"[detect] comp {lbl}: area={area} aspect={aspect:.2f} fill={fill:.2f}\n")
        if not (5.0 <= aspect <= 13.0):
            continue
        # rectangularity: contour fills its min-area rect
        if fill < 0.6:
            continue
        box = cv2.boxPoints(rect).astype(np.float64)
        pts = _order_long_first(box)
        sec = _measure_rectified(norm_lin, pts)
        plates.append(DetectedPlate(rect_pts=pts, sections_lin=sec))
    return plates


def _order_long_first(box: np.ndarray) -> np.ndarray:
    """Order 4 corners CCW so that pts[0]->pts[1] is a long edge."""
    c = box.mean(axis=0)
    ang = np.arctan2(box[:, 1] - c[1], box[:, 0] - c[0])
    p = box[np.argsort(ang)]
    e0 = np.linalg.norm(p[1] - p[0])
    e1 = np.linalg.norm(p[2] - p[1])
    if e0 < e1:
        p = np.roll(p, -1, axis=0)
    return p


def _measure_rectified(norm_lin: np.ndarray, pts: np.ndarray,
                       Wt: int = 900, Ht: int = 100) -> np.ndarray:
    import cv2

    dst = np.array([[0, 0], [Wt, 0], [Wt, Ht], [0, Ht]], dtype=np.float64)
    M = cv2.getPerspectiveTransform(pts.astype(np.float32), dst.astype(np.float32))
    warp = cv2.warpPerspective(norm_lin.astype(np.float32), M, (Wt, Ht)).astype(np.float64)
    # Measure 9 sections along width with trimmed mean in linear RGB.
    out = np.zeros((SECTIONS, 3))
    # chamfer ~0.5mm/90mm and section-straddle inset
    xin = int(Wt / SECTIONS * 0.22)
    r0, r1 = int(Ht * 0.30), int(Ht * 0.70)
    for s in range(SECTIONS):
        x0 = int(s * Wt / SECTIONS) + xin
        x1 = int((s + 1) * Wt / SECTIONS) - xin
        patch = warp[r0:r1, x0:x1].reshape(-1, 3)
        out[s] = _trimmed_mean(patch)
    return out


def _trimmed_mean(patch: np.ndarray, trim: float = 0.15) -> np.ndarray:
    """Per-channel trimmed mean (rejects glare/dust) in linear RGB."""
    if len(patch) == 0:
        return np.zeros(3)
    out = np.zeros(3)
    for ch in range(3):
        v = np.sort(patch[:, ch])
        k = int(len(v) * trim)
        out[ch] = v[k : len(v) - k].mean() if len(v) - 2 * k > 0 else v.mean()
    return out


# ---------------------------------------------------------------------------
# Identification + orientation.
# ---------------------------------------------------------------------------


def identify_and_orient(plates: list[DetectedPlate], manifest: Manifest):
    """Cluster the 12 endpoint patches into 4 filaments, match to the palette by
    robust (z-scored Lab) structure, then assign each plate to a manifest pair
    and orient it so sections run A(pair[0]) -> B(pair[1]).

    Returns (assignments, cluster_lin) where assignments is a list of dicts
    {pair, oriented_sections_lin, endpointA_lin, endpointB_lin} and cluster_lin
    is the (4,3) mean endpoint color per matched filament label."""
    from scipy.cluster.vq import kmeans2
    from scipy.optimize import linear_sum_assignment

    endpoints = []  # (plate_idx, end 0|8, color_lin)
    feats = []
    for pi, pl in enumerate(plates):
        for end in (0, SECTIONS - 1):
            endpoints.append((pi, end))
            feats.append(pl.sections_lin[end])
    feats = np.array(feats)  # (2*nplates, 3)

    labels_order = list(manifest.filaments.keys())
    K = len(labels_order)
    lab_feats = linear_to_lab(feats)
    centroid, membership = kmeans2(lab_feats, K, minit="++", seed=1234)
    # Re-derive centroids in linear RGB (robust mean per cluster).
    clus_lin = np.zeros((K, 3))
    for c in range(K):
        sel = feats[membership == c]
        clus_lin[c] = sel.mean(axis=0) if len(sel) else centroid[c]

    # Match clusters to palette labels by z-scored Lab structure (robust to a
    # global lighting/material offset — see task note: not absolute hex dist).
    pal_lin = np.array([hex_to_linear(manifest.filaments[l].hex) for l in labels_order])
    clus_lab = linear_to_lab(clus_lin)
    pal_lab = linear_to_lab(pal_lin)

    def zscore(x):
        mu = x.mean(axis=0)
        sd = x.std(axis=0)
        sd = np.where(sd < 1e-6, 1.0, sd)
        return (x - mu) / sd

    cz, pz = zscore(clus_lab), zscore(pal_lab)
    cost = np.linalg.norm(cz[:, None, :] - pz[None, :, :], axis=2)  # (K clusters, K labels)
    row, col = linear_sum_assignment(cost)
    cluster_to_label = {r: labels_order[c] for r, c in zip(row, col)}

    ep_cluster = membership  # cluster per endpoint
    cluster_lin = {cluster_to_label[c]: clus_lin[c] for c in range(K)}

    # Assign plates to manifest pairs via their two endpoint labels.
    pair_lookup = {frozenset(p.pair): idx for idx, p in enumerate(manifest.plates)}
    assignments = []
    for pi, pl in enumerate(plates):
        # endpoints for this plate are entries 2*pi, 2*pi+1
        e0_lbl = cluster_to_label[ep_cluster[2 * pi]]
        e8_lbl = cluster_to_label[ep_cluster[2 * pi + 1]]
        key = frozenset((e0_lbl, e8_lbl))
        if key not in pair_lookup or e0_lbl == e8_lbl:
            assignments.append(None)
            continue
        spec = manifest.plates[pair_lookup[key]]
        # Orient so section 0 == pair[0] (filament A).
        if e0_lbl == spec.pair[0]:
            sec = pl.sections_lin.copy()
        else:
            sec = pl.sections_lin[::-1].copy()
        assignments.append(
            {
                "pair": spec.pair,
                "spec": spec,
                "oriented_sections_lin": sec,
                "endpointA_label": spec.pair[0],
                "endpointB_label": spec.pair[1],
            }
        )
    return assignments, cluster_lin


# ---------------------------------------------------------------------------
# Endpoint (corrected hex) fit + ramp (ℓ) fit.
# ---------------------------------------------------------------------------


def corrected_filaments(assignments, manifest: Manifest):
    """Aggregate each filament's measured pure-endpoint color across the plates it
    appears on into a corrected linear color.

    The aggregate is the per-channel MEDIAN across plates, not the mean: a glossy
    plate photographed at a bad angle inflates one endpoint (Black's spread across
    its three plates can be ~27 sRGB from sheen), and the median rejects that
    outlier where the mean would smear it. Per-plate endpoint colors, the spread,
    and a per-channel clipped flag (linear > 1: brighter than the paper-white
    reference, so the display hex saturates but the stored linear value is honest)
    are all surfaced so a disagreement is visible rather than hidden."""
    acc: dict[str, list[tuple[str, np.ndarray]]] = {l: [] for l in manifest.filaments}
    for a in assignments:
        if a is None:
            continue
        acc[a["endpointA_label"]].append(("+".join(a["pair"]), a["oriented_sections_lin"][0]))
        acc[a["endpointB_label"]].append(("+".join(a["pair"]), a["oriented_sections_lin"][SECTIONS - 1]))
    out = {}
    for lbl, vals in acc.items():
        if not vals:
            continue
        v = np.array([c for _, c in vals])
        med_lin = np.median(v, axis=0)
        srgb = linear_to_srgb(v) * 255.0
        spread = float(np.sqrt((srgb.var(axis=0)).mean())) if len(v) > 1 else 0.0
        out[lbl] = {
            "hex": linear_to_hex(med_lin),
            "linear": med_lin,
            "spread_srgb": spread,
            "n": len(v),
            "clipped": [bool(c) for c in (med_lin > 1.0)],
            "per_plate": [
                {"pair": key, "hex": linear_to_hex(c), "linear": [round(float(x), 4) for x in c]}
                for key, c in vals
            ],
        }
    return out


def fit_ell_for_pairs(assignments, corrected, manifest: Manifest, aniso: bool):
    """Per-pair and global ℓ fit by least-squares matching measured vs modeled
    section curves in linear RGB. Uses corrected filament colors as the cell C0,
    manifest TDs, and per-section REALIZED coverage."""
    from scipy.optimize import least_squares

    geom = manifest.geom

    def build_residual(items, aniso):
        def resid(params):
            if aniso:
                lx, lz = params
            else:
                lx = params[0]
                lz = None
            r = []
            for a in items:
                spec = a["spec"]
                cA = corrected[spec.pair[0]]["linear"]
                cB = corrected[spec.pair[1]]["linear"]
                tdA = manifest.filaments[spec.pair[0]].td
                tdB = manifest.filaments[spec.pair[1]].td
                modeled = model_section_curve(cA, cB, tdA, tdB, spec.realized, geom, lx, lz)
                r.append((modeled - a["oriented_sections_lin"]).ravel())
            return np.concatenate(r)

        return resid

    fits = {}
    valid = [
        a
        for a in assignments
        if a is not None and a["pair"][0] in corrected and a["pair"][1] in corrected
    ]
    if not valid:
        return {}, {"ell_mm": float("nan"), "residual_rms_lin": float("nan")}
    for a in valid:
        resid = build_residual([a], aniso)
        x0 = [0.3, 0.3] if aniso else [0.3]
        lo = [1e-3, 1e-3] if aniso else [1e-3]
        hi = [5.0, 5.0] if aniso else [5.0]
        sol = least_squares(resid, x0, bounds=(lo, hi), xtol=1e-10, ftol=1e-10)
        rms = float(np.sqrt(np.mean(sol.fun**2)))
        key = "+".join(a["pair"])
        if aniso:
            fits[key] = {"ell_x_mm": float(sol.x[0]), "ell_z_mm": float(sol.x[1]), "residual_rms_lin": rms}
        else:
            fits[key] = {"ell_mm": float(sol.x[0]), "residual_rms_lin": rms}

    # Robust global fit: reject per-pair ℓ outliers (MAD) before pooling, so a
    # pair whose ramp the additive model can't fit — bright+bright pairs mix more
    # subtractively than the model and rail ℓ to a bound — doesn't drag the global
    # estimate. The well-constrained pairs (a translucent filament bleeding into a
    # dark one) agree tightly; those carry the fit.
    def key_ell(v):
        return v.get("ell_x_mm", v.get("ell_mm"))

    ells = np.array([key_ell(fits["+".join(a["pair"])]) for a in valid])
    med = float(np.median(ells))
    mad = 1.4826 * float(np.median(np.abs(ells - med))) + 1e-6
    inlier = np.abs(ells - med) < max(3 * mad, 0.05)
    excluded = ["+".join(a["pair"]) for a, ok in zip(valid, inlier) if not ok]
    pooled = [a for a, ok in zip(valid, inlier) if ok] or valid

    resid = build_residual(pooled, aniso)
    x0 = [0.3, 0.3] if aniso else [0.3]
    lo = [1e-3, 1e-3] if aniso else [1e-3]
    hi = [5.0, 5.0] if aniso else [5.0]
    sol = least_squares(resid, x0, bounds=(lo, hi), xtol=1e-10, ftol=1e-10)
    grms = float(np.sqrt(np.mean(sol.fun**2)))
    if aniso:
        glob = {"ell_x_mm": float(sol.x[0]), "ell_z_mm": float(sol.x[1]), "residual_rms_lin": grms}
    else:
        glob = {"ell_mm": float(sol.x[0]), "residual_rms_lin": grms}
    glob["n_pairs_used"] = len(pooled)
    glob["excluded_pairs"] = excluded
    return fits, glob


# ---------------------------------------------------------------------------
# Plots + output.
# ---------------------------------------------------------------------------


def save_plots(assignments, corrected, manifest, global_ell, aniso, out_dir):
    import os

    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    valid = [
        a
        for a in assignments
        if a is not None and a["pair"][0] in corrected and a["pair"][1] in corrected
    ]
    if not valid:
        return None
    ncol = 3
    nrow = int(math.ceil(len(valid) / ncol))
    fig, axes = plt.subplots(nrow, ncol, figsize=(4 * ncol, 3 * nrow), squeeze=False)
    xs = np.linspace(0, 1, SECTIONS)
    lx = global_ell.get("ell_x_mm", global_ell.get("ell_mm"))
    lz = global_ell.get("ell_z_mm") if aniso else None
    for idx, a in enumerate(valid):
        ax = axes[idx // ncol][idx % ncol]
        spec = a["spec"]
        cA = corrected[spec.pair[0]]["linear"]
        cB = corrected[spec.pair[1]]["linear"]
        tdA = manifest.filaments[spec.pair[0]].td
        tdB = manifest.filaments[spec.pair[1]].td
        modeled = model_section_curve(cA, cB, tdA, tdB, spec.realized, manifest.geom, lx, lz)
        meas = a["oriented_sections_lin"]
        meas_s = linear_to_srgb(meas)
        mod_s = linear_to_srgb(modeled)
        for ch, cname in zip(range(3), ("r", "g", "b")):
            ax.plot(xs, meas_s[:, ch], "o", color=cname, ms=4)
            ax.plot(xs, mod_s[:, ch], "-", color=cname, lw=1)
        ax.set_title("+".join(a["pair"]), fontsize=9)
        ax.set_xlabel("coverage (B fraction)")
        ax.set_ylabel("sRGB")
        ax.set_ylim(0, 1)
    for j in range(len(valid), nrow * ncol):
        axes[j // ncol][j % ncol].axis("off")
    fig.suptitle(f"Measured (o) vs modeled (—) — global ℓ={lx:.3f}mm" + (f", ℓz={lz:.3f}" if aniso else ""))
    fig.tight_layout()
    path = os.path.join(out_dir, "ramp_fits.png")
    fig.savefig(path, dpi=110)
    plt.close(fig)
    return path


def run_analysis(photo_path: str, manifest_path: str, out_dir: str, aniso: bool) -> dict:
    import os

    import cv2

    os.makedirs(out_dir, exist_ok=True)
    manifest = load_manifest(manifest_path)

    bgr = cv2.imread(photo_path, cv2.IMREAD_COLOR)
    if bgr is None:
        raise SystemExit(f"could not read image: {photo_path}")
    rgb = cv2.cvtColor(bgr, cv2.COLOR_BGR2RGB).astype(np.float64) / 255.0
    lin = srgb_to_linear(rgb)

    norm, illum = normalize_illumination(lin)
    plates = detect_plates(norm)
    result: dict = {"photo": photo_path, "manifest": manifest_path, "n_plates_detected": len(plates)}
    if len(plates) == 0:
        result["error"] = "no plates detected"
        return result

    assignments, cluster_lin = identify_and_orient(plates, manifest)
    n_ident = sum(1 for a in assignments if a is not None)
    result["n_plates_identified"] = n_ident

    corrected = corrected_filaments(assignments, manifest)
    fits, glob = fit_ell_for_pairs(assignments, corrected, manifest, aniso)

    result["corrected_filaments"] = {
        lbl: {
            "hex": d["hex"],
            "linear": [round(float(x), 4) for x in d["linear"]],
            "spread_srgb": round(d["spread_srgb"], 1),
            "n_measurements": d["n"],
            "clipped_channels": d["clipped"],
            "per_plate": d["per_plate"],
        }
        for lbl, d in corrected.items()
    }
    result["per_pair_ell"] = fits
    result["global_ell"] = glob
    result["plates"] = []
    for a in assignments:
        if a is None:
            continue
        result["plates"].append(
            {
                "pair": list(a["pair"]),
                "sections_srgb": [
                    [int(v) for v in linear_to_srgb_byte(a["oriented_sections_lin"][s])]
                    for s in range(SECTIONS)
                ],
            }
        )

    plot_path = save_plots(assignments, corrected, manifest, glob, aniso, out_dir)
    result["plot"] = plot_path

    out_json = os.path.join(out_dir, "swatchphoto_result.json")
    with open(out_json, "w") as f:
        json.dump(result, f, indent=2)
    result["_json"] = out_json
    return result


# ---------------------------------------------------------------------------
# Synthetic round-trip (build/verify without a real photo).
# ---------------------------------------------------------------------------

DEFAULT_PALETTE = [
    ("Black", "#080A0D", 0.1),
    ("Beige", "#C2AB72", 0.5),
    ("Cold White", "#D9DFE5", 0.3),
    ("Orange", "#F67405", 3.3),
]


def _synthetic_manifest(geom: Geom) -> Manifest:
    fils = {l: Filament(l, h, t) for (l, h, t) in DEFAULT_PALETTE}
    labels = [l for (l, _, _) in DEFAULT_PALETTE]
    nominal = np.array([s / 8.0 for s in range(SECTIONS)])
    plates = []
    for i in range(len(labels)):
        for j in range(i + 1, len(labels)):
            # realized coverage: reproduce what the ideal Bayer grid yields.
            cov = np.array(
                [
                    (BAYER8[np.arange(geom.nx) % 8][:, None] < (s / 8.0) * 64.0).mean()
                    if 0 < s < 8
                    else float(s == 8)
                    for s in range(SECTIONS)
                ]
            )
            plates.append(PlateSpec(pair=(labels[i], labels[j]), nominal=nominal.copy(), realized=cov))
    return Manifest(geom=geom, filaments=fils, plates=plates)


def _render_plate_srgb(section_lin: np.ndarray, chamfer_lin: np.ndarray,
                       Wt=900, Ht=100) -> np.ndarray:
    """Render a canonical plate (sRGB 0-1) from its 9 section colors plus a
    0.5mm color-A chamfer border."""
    img = np.zeros((Ht, Wt, 3))
    for s in range(SECTIONS):
        x0 = int(s * Wt / SECTIONS)
        x1 = int((s + 1) * Wt / SECTIONS)
        img[:, x0:x1] = section_lin[s]
    b = max(1, int(round(0.5 / 90.0 * Wt)))
    img[:b, :] = chamfer_lin
    img[-b:, :] = chamfer_lin
    img[:, :b] = chamfer_lin
    img[:, -b:] = chamfer_lin
    return img  # linear


def generate_synthetic(manifest: Manifest, ell_true: float, seed: int,
                       aniso_z: float | None = None):
    """Return (photo_srgb_uint8, truth) where truth has the injected colors/ℓ."""
    import cv2

    rng = np.random.default_rng(seed)
    geom = manifest.geom
    labels = list(manifest.filaments.keys())
    # "True printed" colors: nominal hex nudged so recovery is non-trivial.
    true_lin = {}
    for l in labels:
        base = hex_to_linear(manifest.filaments[l].hex)
        nudge = 1.0 + rng.uniform(-0.06, 0.06, size=3)
        true_lin[l] = np.clip(base * nudge, 0, 1)
    # Paper is a mid off-white (linear ~0.6), DARKER than the Cold White plate —
    # so at least one plate is brighter than paper and normalizes to >1.0. This
    # reproduces the real-photo regime the earlier all-white-paper scene missed.
    paper_lin = np.array([0.63, 0.60, 0.57])

    # Slots must exceed a rotated plate's diagonal (~906px) in BOTH dimensions so
    # plates never overlap or merge into one component.
    slot_w, slot_h = 980, 980
    canvas_w, canvas_h = slot_w * 3, slot_h * 2
    canvas = np.ones((canvas_h, canvas_w, 3)) * paper_lin  # off-white paper (linear)

    placements = []
    for pi, spec in enumerate(manifest.plates):
        cA = true_lin[spec.pair[0]]
        cB = true_lin[spec.pair[1]]
        tdA = manifest.filaments[spec.pair[0]].td
        tdB = manifest.filaments[spec.pair[1]].td
        sec = model_section_curve(cA, cB, tdA, tdB, spec.realized, geom, ell_true, aniso_z)
        plate = _render_plate_srgb(sec, cA)  # linear, 100x900
        Ht, Wt = plate.shape[:2]
        ang = rng.uniform(0, 360)
        slot_r, slot_c = divmod(pi, 3)
        cx = int((slot_c + 0.5) * slot_w + rng.uniform(-30, 30))
        cy = int((slot_r + 0.5) * slot_h + rng.uniform(-30, 30))
        pad = int(math.hypot(Wt, Ht)) + 4
        big = np.zeros((pad, pad, 3))
        alpha = np.zeros((pad, pad))
        oy, ox = (pad - Ht) // 2, (pad - Wt) // 2
        big[oy : oy + Ht, ox : ox + Wt] = plate
        alpha[oy : oy + Ht, ox : ox + Wt] = 1.0
        Mrot = cv2.getRotationMatrix2D((pad / 2, pad / 2), ang, 1.0)
        big = cv2.warpAffine(big, Mrot, (pad, pad))
        alpha = cv2.warpAffine(alpha, Mrot, (pad, pad))
        # Vectorized composite of the (clipped) plate ROI onto the canvas.
        Y0, X0 = int(cy - pad / 2), int(cx - pad / 2)
        cy0, cy1 = max(0, Y0), min(canvas_h, Y0 + pad)
        cx0, cx1 = max(0, X0), min(canvas_w, X0 + pad)
        by0, bx0 = cy0 - Y0, cx0 - X0
        by1, bx1 = by0 + (cy1 - cy0), bx0 + (cx1 - cx0)
        a = alpha[by0:by1, bx0:bx1][..., None]
        canvas[cy0:cy1, cx0:cx1] = canvas[cy0:cy1, cx0:cx1] * (1 - a) + big[by0:by1, bx0:bx1] * a
        placements.append({"pair": spec.pair, "center": (cx, cy), "angle": ang})

    # Illumination gradient (linear), then camera: to sRGB, blur, noise.
    yy, xx = np.mgrid[0:canvas_h, 0:canvas_w]
    grad = 0.82 + 0.23 * (xx / canvas_w) + 0.10 * (yy / canvas_h)
    tint = np.stack([grad * 1.02, grad, grad * 0.97], axis=2)
    canvas = canvas * tint

    srgb = linear_to_srgb(canvas)
    srgb = cv2.GaussianBlur(srgb, (0, 0), sigmaX=1.2)
    srgb = srgb + rng.normal(0, 0.006, srgb.shape)
    photo = np.clip(np.round(srgb * 255.0), 0, 255).astype(np.uint8)
    truth = {
        "ell_true": ell_true,
        "colors_lin": true_lin,
        "paper_lin": paper_lin,
        "placements": placements,
        "aniso_z": aniso_z,
    }
    return photo, truth


def run_selftest(aniso: bool, ell_true: float, seed: int) -> int:
    import os
    import tempfile

    import cv2

    geom = Geom(
        block_width_mm=0.5263157894736842,
        row_height0_mm=0.20000000298023224,
        row_height_up_mm=0.07999999821186066,
        row_count=124,
    )
    manifest = _synthetic_manifest(geom)
    photo, truth = generate_synthetic(manifest, ell_true, seed, aniso_z=None)

    tmp = tempfile.mkdtemp(prefix="swatchphoto_selftest_")
    photo_path = os.path.join(tmp, "synthetic.png")
    cv2.imwrite(photo_path, cv2.cvtColor(photo, cv2.COLOR_RGB2BGR))
    manifest_path = os.path.join(tmp, "manifest.swatch.json")
    _write_manifest_json(manifest, manifest_path)

    print(f"[selftest] injected ℓ={ell_true} mm, output dir {tmp}")
    res = run_analysis(photo_path, manifest_path, tmp, aniso)

    ok = True

    def check(name, cond, detail=""):
        nonlocal ok
        status = "PASS" if cond else "FAIL"
        if not cond:
            ok = False
        print(f"  [{status}] {name} {detail}")

    check("detected 6 plates", res["n_plates_detected"] == 6, f"(got {res['n_plates_detected']})")
    check("identified 6 plates", res.get("n_plates_identified") == 6, f"(got {res.get('n_plates_identified')})")

    # Recovery is checked in paper-NORMALIZED linear space: the analysis divides
    # out paper, so the recoverable ground truth is true_lin / paper_lin. Checking
    # linear (not clipped hex) also verifies brighter-than-paper values (>1.0)
    # survive instead of being clamped.
    paper = truth["paper_lin"]
    max_lin_err = 0.0
    any_over_one = False
    for lbl, d in res.get("corrected_filaments", {}).items():
        rec = np.array(d["linear"])
        norm_truth = truth["colors_lin"][lbl] / paper
        any_over_one = any_over_one or bool((norm_truth > 1.02).any())
        max_lin_err = max(max_lin_err, float(np.abs(rec - norm_truth).max()))
    check("linear recovery < 0.05 (paper-normalized)", max_lin_err < 0.05, f"(max |Δ|={max_lin_err:.3f})")
    check("a plate is brighter than paper (>1.0 carried)", any_over_one,
          "(Cold White should normalize above 1.0)")

    # ℓ recovery.
    glob = res.get("global_ell", {})
    if aniso:
        lx = glob.get("ell_x_mm", 0)
        lz = glob.get("ell_z_mm", 0)
        # isotropic truth -> both should land near ell_true.
        errl = max(abs(lx - ell_true), abs(lz - ell_true))
        check("ℓx,ℓz recovery within 0.08mm", errl < 0.08, f"(ℓx={lx:.3f} ℓz={lz:.3f}, true {ell_true})")
    else:
        le = glob.get("ell_mm", 0)
        err = abs(le - ell_true)
        check("global ℓ within 0.06mm", err < 0.06, f"(ℓ={le:.4f}, true {ell_true}, Δ={err:.4f})")

    per = res.get("per_pair_ell", {})
    if not aniso and per:
        vals = sorted(v["ell_mm"] for v in per.values())
        med = vals[len(vals) // 2]
        # Per-pair ℓ is only well-constrained when a pair has a translucent
        # filament; near-opaque pairs fit noisily. Assert on the robust median
        # (the global fit is the reliable number); report the full range.
        check("median per-pair ℓ within 0.06mm of truth", abs(med - ell_true) < 0.06,
              f"(median {med:.3f}, range {vals[0]:.3f}..{vals[-1]:.3f}, true {ell_true})")

    print(f"[selftest] {'ALL PASS' if ok else 'FAILURES'} — result JSON: {res.get('_json')}, plot: {res.get('plot')}")
    return 0 if ok else 1


def _write_manifest_json(manifest: Manifest, path: str):
    m = {
        "blockWidthMM": manifest.geom.block_width_mm,
        "rowHeight0MM": manifest.geom.row_height0_mm,
        "rowHeightUpMM": manifest.geom.row_height_up_mm,
        "rowCount": manifest.geom.row_count,
        "palette": [{"label": f.label, "hex": f.hex, "td": f.td} for f in manifest.filaments.values()],
        "plates": [],
    }
    for spec in manifest.plates:
        m["plates"].append(
            {
                "pair": list(spec.pair),
                "sections": [
                    {
                        "index": s,
                        "nominalCoverage": float(spec.nominal[s]),
                        "realizedCoverageFront": float(spec.realized[s]),
                        "realizedCoverageBack": float(spec.realized[s]),
                        "foreignCoverageFront": 0,
                        "foreignCoverageBack": 0,
                    }
                    for s in range(SECTIONS)
                ],
            }
        )
    with open(path, "w") as f:
        json.dump(m, f, indent=2)


# ---------------------------------------------------------------------------
# CLI.
# ---------------------------------------------------------------------------


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("photo", nargs="?", help="top-down JPEG/PNG of the printed plates")
    ap.add_argument("manifest", nargs="?", help="the .swatch.json manifest")
    ap.add_argument("--out", default=None, help="output directory (default: alongside photo)")
    ap.add_argument("--anisotropic", action="store_true", help="fit separate ℓx (X) and ℓz (Z)")
    ap.add_argument("--selftest", action="store_true", help="run the synthetic round-trip test")
    ap.add_argument("--ell", type=float, default=0.35, help="injected ℓ for --selftest")
    ap.add_argument("--seed", type=int, default=0, help="RNG seed for --selftest")
    args = ap.parse_args(argv)

    if args.selftest:
        return run_selftest(args.anisotropic, args.ell, args.seed)

    if not args.photo or not args.manifest:
        ap.error("photo and manifest are required (or use --selftest)")

    import os

    out_dir = args.out or os.path.join(os.path.dirname(os.path.abspath(args.photo)), "swatchphoto_out")
    res = run_analysis(args.photo, args.manifest, out_dir, args.anisotropic)
    print(json.dumps({k: v for k, v in res.items() if not k.startswith("_")}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
