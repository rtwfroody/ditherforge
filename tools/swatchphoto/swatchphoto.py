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
gloss), with per-plate values reported.

MANUFACTURER-ANCHORED fit (default, --anchor mfg): a phone JPEG's absolute colors
are distrusted (auto WB, HDR tone mapping, paper-as-white). So each plate's
measured endpoints are affine-mapped onto the manifest's manufacturer hexes and
only the RELATIVE mixing shape is fit; the photo-absolute colors are kept as a
labeled diagnostic. --anchor photo restores the old photo-absolute behavior.

CANDIDATE MIXING MODELS: the additive blend can't express the printed speckle's
subtractive darkening, so several refinements are fit and compared (each a LOCAL
per-cell rule with per-FILAMENT parameters only, so the winner can port to
voxel.EffectiveCellColors). Section averaging always stays LINEAR (camera/eye);
only the per-cell mixing changes:
  additive            (ℓ)        : the current Go model. γ=1 regression baseline.
  global_gamma        (ℓ, γ)     : power-mean domain f(c)=c^γ, uniform γ.
  td_gamma            (ℓ, a, b)  : per-cell γ_i = clamp(a + b·log10(TD_i)).
  transmittance       (ℓ, κ)     : neighbor light filtered by the cell's own hue,
                                   T_i=(C0_i/max)^κ, applied to the neighbor's
                                   deviation from C0 (κ=0 → additive).
  transmittance_shadow(ℓ, κ, s)  : + interface micro-shadowing ×(1−s·u_i).
  td_gamma_shadow     (ℓ, a,b,s) : + micro-shadowing on td_gamma.
Ranked by LEAVE-ONE-PAIR-OUT cross-validation (primary), with a parsimony rule
(fewest params within a small LOO margin of the best). γ=1 / additive is
regression-guarded bit-for-bit.

The base blend model is ported faithfully from the Go source:
  internal/voxel/td_layered.go   (EffectiveCellColors: 2 Jacobi passes)
  internal/palette/td.go         (NeighborLeak: β = 10^(−ℓ/TD), clamps)
  internal/voxel/color.go        (BuildNeighbors: 26-conn weights 1.0/0.1/0.01)
  internal/voxel/color.go        (sRGB<->linear)

Usage:
  uv run tools/swatchphoto/swatchphoto.py PHOTO.jpg MANIFEST.swatch.json [--out DIR] [--anchor mfg|photo] [--anisotropic]
  uv run tools/swatchphoto/swatchphoto.py --selftest [--anisotropic] [--ell 0.35] [--gamma 0.4] [--seed 0]

The --selftest mode generates synthetic "photos" from the manufacturer colors
through the same forward model at a known injected (γ, ℓ), with a per-plate
affine distortion (WB/gloss) that anchoring must invert (random rotations/
positions, an illumination gradient, blur and sensor noise), and asserts the chain
recovers the plates, colors and ℓ. Build/verify the tool with --selftest before
a real photo exists.
"""

from __future__ import annotations

import argparse
import itertools
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


GAMMA_CLAMP = (-3.0, 1.5)  # per-cell γ is clamped to this range


def _cell_area(geom: Geom, rows: int, cols: int) -> np.ndarray:
    return np.maximum(np.broadcast_to((geom.row_heights() * geom.block_width_mm)[:, None], (rows, cols)), 1e-6)


def _unlike_fraction(pattern: np.ndarray, area: np.ndarray) -> np.ndarray:
    """Per cell, the fraction of its neighbor weight (weight × area_j) that belongs
    to cells of the OTHER filament — the local unlike-interface density that
    candidate 3's micro-shadowing scales with. Depends only on the pattern."""
    patf = pattern.astype(np.float64)
    uw = np.zeros(pattern.shape)
    tw = np.zeros(pattern.shape)
    for dr, dc, w in _OFFSETS:
        npat, valid = _shift(patf, dr, dc)
        narea, _ = _shift(area, dr, dc)
        wij = w * narea * valid
        tw += wij
        uw += wij * ((npat != patf) & valid)
    return uw / np.maximum(tw, 1e-9)


def _blend_gamma(c0_lin, beta, area, gamma_grid, iterations):
    """Candidate 1 / power-mean core. Per cell i the neighbor blend runs in that
    cell's OWN domain f_i(c) = c^{γ_i}: read neighbors in linear, raise to γ_i,
    weighted-mean, blend with the (1−β) self term (also in γ_i-space, self = C0),
    then invert per cell. The running state stays LINEAR, so the rule is purely
    local — each cell only ever needs its neighbors' linear colors and its own γ_i.
    Uniform γ_i reproduces the global power-mean model."""
    rows, cols = c0_lin.shape[:2]
    g = gamma_grid[..., None]
    lo, hi = 1e-4, 1e-6
    cf_self = np.maximum(c0_lin, lo) ** g
    cur = c0_lin.copy()
    for _ in range(iterations):
        wsum = np.zeros((rows, cols))
        ssum = np.zeros((rows, cols, 3))
        for dr, dc, w in _OFFSETS:
            nv, valid = _shift(cur, dr, dc)
            narea, _ = _shift(area, dr, dc)
            wij = w * narea * valid
            wsum += wij
            ssum += wij[..., None] * (np.maximum(nv, lo) ** g)
        active = (wsum > 0) & (beta > 0)
        mean = np.zeros_like(ssum)
        nz = wsum > 0
        mean[nz] = ssum[nz] / wsum[nz][..., None]
        cf = (1.0 - beta)[..., None] * cf_self + beta[..., None] * mean
        blended = np.maximum(cf, hi) ** (1.0 / g)
        cur = np.where(active[..., None], blended, c0_lin)
    return cur


def _blend_transmittance(c0_lin, beta, area, kappa, iterations):
    """Candidate 2 core (linear domain). Neighbor light returning to cell i passes
    through cell i's translucent body and is filtered by its per-channel
    transmittance color T_i. We filter the neighbor's DEVIATION from the cell's own
    color (not the raw neighbor sum):

        effective_i = C0_i + β_i · ( T_i ∘ (nbAvg_i − C0_i) ),   T_i,c = (C0_i,c / max_c C0_i,c)^κ

    κ=0 → T_i = 1 → the additive model (1−β)C0 + β·nbAvg exactly. Filtering the
    deviation (rather than the literal (1−β)C0 + β·T∘nbAvg) keeps a UNIFORM patch
    at its own color — essential so the anchored endpoints (sections 0 and 8) stay
    on the manufacturer hexes; the literal form self-filters pure patches and its
    endpoint shift fights the affine anchoring, pinning κ→0. Larger κ still tints
    cross-color neighbor light by the cell's hue, so orange next to white keeps its
    saturated low green/blue instead of washing out — an asymmetry no scalar γ has."""
    rows, cols = c0_lin.shape[:2]
    maxc = np.maximum(c0_lin.max(axis=2, keepdims=True), 1e-6)
    T = (c0_lin / maxc) ** kappa  # (rows,cols,3)
    cur = c0_lin.copy()
    for _ in range(iterations):
        wsum = np.zeros((rows, cols))
        ssum = np.zeros((rows, cols, 3))
        for dr, dc, w in _OFFSETS:
            nv, valid = _shift(cur, dr, dc)
            narea, _ = _shift(area, dr, dc)
            wij = w * narea * valid
            wsum += wij
            ssum += wij[..., None] * nv
        active = (wsum > 0) & (beta > 0)
        nbAvg = np.zeros_like(ssum)
        nz = wsum > 0
        nbAvg[nz] = ssum[nz] / wsum[nz][..., None]
        blended = c0_lin + beta[..., None] * (T * (nbAvg - c0_lin))
        cur = np.where(active[..., None], blended, c0_lin)
    return cur


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
    gamma: float = 1.0,
    gamma_ab: tuple | None = None,
    kappa: float | None = None,
    shadow_s: float = 0.0,
) -> np.ndarray:
    """Return (rows, cols, 3) linear-light per-cell simulated colors.

    Base is the faithful port of voxel.EffectiveCellColors: C_{t+1}(i) =
    (1−β)·C0(i) + β·(Σ_j w_ij·C_t(j))/(Σ_j w_ij), self term always C0, w_ij =
    weight × max(area_j, ε), β = 10^(−ℓ/TD), 2 passes, LINEAR light. Callers
    average the returned per-cell linear colors, so spatial averaging of the fine
    speckle stays additive (as camera/eye do). γ=1, no gamma_ab/kappa/shadow is the
    exact additive model, bit-for-bit.

    Candidate refinements (each a LOCAL per-cell rule on the neighbor graph, with
    per-FILAMENT parameters only, so the winner can port to Go):
      gamma / gamma_ab : power-mean domain (candidate 1). gamma is a uniform
        exponent; gamma_ab=(a,b) makes it per-cell γ_i = clamp(a + b·log10(TD_i))
        so translucency sets how subtractive the cell mixes. See _blend_gamma.
      kappa            : transmittance-filtered neighbor term (candidate 2, linear
        domain). See _blend_transmittance.
      shadow_s         : interface micro-shadowing (candidate 3), a post-multiply
        effective_i *= (1 − s·u_i) with u_i the unlike-neighbor weight fraction —
        combinable with either base.

    Anisotropy (ell_z != ell_x, additive only) keeps the direction-dependent leak
    for the exploratory --anisotropic mode."""
    rows, cols = pattern.shape
    aniso = ell_z is not None and abs(ell_z - ell_x) > 1e-12
    if ell_z is None:
        ell_z = ell_x

    c0_lin = np.where(pattern[..., None] == 1, colB_lin[None, None, :], colA_lin[None, None, :]).astype(np.float64)
    td_grid = np.where(pattern == 1, tdB, tdA).astype(np.float64)
    beta_iso = neighbor_leak_grid(td_grid, ell_x)
    area = _cell_area(geom, rows, cols)

    if kappa is not None:
        cur = _blend_transmittance(c0_lin, beta_iso, area, kappa, iterations)
    elif gamma_ab is not None or abs(gamma - 1.0) > 1e-12:
        if gamma_ab is not None:
            a, b = gamma_ab
            gg = np.clip(a + b * np.log10(td_grid), GAMMA_CLAMP[0], GAMMA_CLAMP[1])
        else:
            gg = np.full((rows, cols), gamma)
        cur = _blend_gamma(c0_lin, beta_iso, area, gg, iterations)
    else:
        # Additive (and anisotropic) core — bit-identical to the original.
        beta_by_dir = {}
        if aniso:
            for ell_dir in {ell_x, ell_z, math.hypot(ell_x, ell_z)}:
                beta_by_dir[ell_dir] = neighbor_leak_grid(td_grid, ell_dir)
        cur = c0_lin.copy()
        for _ in range(iterations):
            wsum = np.zeros((rows, cols))
            ssum = np.zeros((rows, cols, 3))
            for dr, dc, w in _OFFSETS:
                nv, valid = _shift(cur, dr, dc)
                narea, _ = _shift(area, dr, dc)
                wij = w * narea * valid
                wsum += wij
                if not aniso:
                    ssum += wij[..., None] * nv
                else:
                    ell_dir = math.hypot(ell_x, ell_z) if (dr and dc) else (ell_x if dc else ell_z)
                    ssum += (wij * beta_by_dir[ell_dir])[..., None] * (nv - c0_lin)
            if not aniso:
                active = (wsum > 0) & (beta_iso > 0)
                mean = np.zeros_like(ssum)
                nz = wsum > 0
                mean[nz] = ssum[nz] / wsum[nz][..., None]
                cur = np.where(active[..., None], (1.0 - beta_iso)[..., None] * c0_lin + beta_iso[..., None] * mean, c0_lin)
            else:
                nz = wsum > 0
                add = np.zeros_like(ssum)
                add[nz] = ssum[nz] / wsum[nz][..., None]
                cur = c0_lin + add

    if shadow_s > 0:
        cur = cur * (1.0 - shadow_s * _unlike_fraction(pattern, area))[..., None]
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
                        ell_x, ell_z=None, gamma=1.0, gamma_ab=None, kappa=None,
                        shadow_s=0.0) -> np.ndarray:
    """Full forward model: pattern -> per-cell blend -> per-section centre colors
    (9,3), averaged LINEARLY. Extra kwargs select the candidate mixing model
    (see effective_cell_colors)."""
    pattern = build_pattern(section_coverage, geom)
    grid = effective_cell_colors(pattern, covA_lin, covB_lin, tdA, tdB, geom, ell_x, ell_z,
                                 gamma=gamma, gamma_ab=gamma_ab, kappa=kappa, shadow_s=shadow_s)
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


ANCHOR_CONTRAST_THRESH = 0.08  # min per-channel endpoint separation (linear) to trust a channel


def anchor_sections(oriented_lin: np.ndarray, a_mfg: np.ndarray, b_mfg: np.ndarray):
    """Affine-map a plate's measured linear sections, per channel, so its own
    measured endpoints (sections 0 and 8) land exactly on the manufacturer hexes:

        x' = A_mfg + (x − mA) · (B_mfg − A_mfg) / (mB − mA)

    This cancels white balance, flare, paper color and per-plate black gloss (each
    plate's black maps to manufacturer black regardless of sheen), keeping only the
    RELATIVE curve shape the phone JPEG is trusted for. Returns (anchored (9,3),
    weight (3,)): a channel whose endpoints barely differ (|mB−mA| below the
    contrast threshold, e.g. Cold White + Orange in red) would divide by ~0, so it
    is dropped (weight 0) rather than amplified into noise."""
    mA = oriented_lin[0]
    mB = oriented_lin[SECTIONS - 1]
    denom = mB - mA
    w = (np.abs(denom) >= ANCHOR_CONTRAST_THRESH).astype(np.float64)
    safe = np.where(np.abs(denom) < 1e-9, 1.0, denom)
    anchored = a_mfg[None, :] + (oriented_lin - mA[None, :]) * (b_mfg - a_mfg)[None, :] / safe[None, :]
    return anchored, w


def _pair_fit_inputs(assignments, corrected, manifest: Manifest, anchor_mode: str):
    """Build per-pair fit inputs: endpoints (A,B linear), TDs, realized coverage,
    the target section curve, and per-channel weights.

    anchor_mode "mfg" (default): endpoints are the manufacturer palette hexes and
    the target is the plate's measured curve affine-anchored onto them — the fit
    then only sees the RELATIVE mixing shape. "photo": endpoints are the median
    photo-absolute measured colors and the target is the raw measured curve (the
    old, paper-anchored behavior)."""
    items = []
    for a in assignments:
        if a is None:
            continue
        spec = a["spec"]
        p0, p1 = spec.pair
        if anchor_mode == "mfg":
            A = hex_to_linear(manifest.filaments[p0].hex)
            B = hex_to_linear(manifest.filaments[p1].hex)
            target, w = anchor_sections(a["oriented_sections_lin"], A, B)
        else:
            if p0 not in corrected or p1 not in corrected:
                continue
            A = corrected[p0]["linear"]
            B = corrected[p1]["linear"]
            target = a["oriented_sections_lin"]
            w = np.ones(3)
        items.append({
            "pair": (p0, p1), "A": A, "B": B,
            "tdA": manifest.filaments[p0].td, "tdB": manifest.filaments[p1].td,
            "cov": spec.realized, "target": target, "w": w,
        })
    return items


def _rms(v):
    return float(np.sqrt(np.mean(v**2))) if len(v) else float("nan")


# Candidate mixing models. Each is a LOCAL per-cell rule on the neighbor graph with
# per-FILAMENT parameters only (so the winner ports to voxel.EffectiveCellColors);
# per-pair values may only arise as functions of the filaments' own TDs/colors.
# grid = coarse per-parameter point count for the deterministic init.
MODEL_CANDIDATES = [
    dict(name="additive", pnames=["ell"], lo=[1e-3], hi=[5.0], grid=[10],
         kw=lambda p: dict(ell_x=p[0])),
    dict(name="global_gamma", pnames=["ell", "gamma"], lo=[1e-3, -2.0], hi=[5.0, 1.2], grid=[7, 8],
         kw=lambda p: dict(ell_x=p[0], gamma_ab=(p[1], 0.0))),
    # td_gamma warm-starts from global_gamma (a=γ, b=0), so no 3-D grid needed.
    dict(name="td_gamma", pnames=["ell", "a", "b"], lo=[1e-3, -2.0, -1.5], hi=[5.0, 1.5, 1.5], grid=[5, 5, 5],
         kw=lambda p: dict(ell_x=p[0], gamma_ab=(p[1], p[2])),
         warm_from=("global_gamma", lambda q: [q[0], q[1], 0.0])),
    dict(name="transmittance", pnames=["ell", "kappa"], lo=[1e-3, 0.0], hi=[5.0, 5.0], grid=[7, 8],
         kw=lambda p: dict(ell_x=p[0], kappa=p[1])),
    # +shadow candidates warm-start from their base (s=0), so no extra grid dim.
    dict(name="transmittance_shadow", pnames=["ell", "kappa", "s"], lo=[1e-3, 0.0, 0.0], hi=[5.0, 5.0, 0.5], grid=[5, 5, 4],
         kw=lambda p: dict(ell_x=p[0], kappa=p[1], shadow_s=p[2]),
         warm_from=("transmittance", lambda q: [q[0], q[1], 0.0])),
    dict(name="td_gamma_shadow", pnames=["ell", "a", "b", "s"], lo=[1e-3, -2.0, -1.5, 0.0], hi=[5.0, 1.5, 1.5, 0.5], grid=[4, 4, 4, 3],
         kw=lambda p: dict(ell_x=p[0], gamma_ab=(p[1], p[2]), shadow_s=p[3]),
         warm_from=("td_gamma", lambda q: [q[0], q[1], q[2], 0.0])),
]


def _cand_resid(items, geom, cand, params):
    kw = cand["kw"](params)
    r = []
    for it in items:
        model = model_section_curve(it["A"], it["B"], it["tdA"], it["tdB"], it["cov"], geom, **kw)
        r.append(((model - it["target"]) * it["w"][None, :]).ravel())
    return np.concatenate(r) if r else np.zeros(0)


def _fit_cand(items, geom, cand, warm=None, max_nfev=None):
    """Deterministic fit of one candidate: coarse grid (skipped when warm-started)
    + bounded least-squares polish. Returns (params, rms)."""
    from scipy.optimize import least_squares

    if warm is None:
        grids = [np.linspace(lo, hi, n) for lo, hi, n in zip(cand["lo"], cand["hi"], cand["grid"])]
        best, best_ssr = None, np.inf
        for combo in itertools.product(*grids):
            ssr = float(np.sum(_cand_resid(items, geom, cand, combo) ** 2))
            if ssr < best_ssr:
                best, best_ssr = combo, ssr
        x0 = list(best)
    else:
        x0 = [min(max(v, lo), hi) for v, lo, hi in zip(warm, cand["lo"], cand["hi"])]
    sol = least_squares(lambda p: _cand_resid(items, geom, cand, p), x0,
                        bounds=(cand["lo"], cand["hi"]), xtol=1e-9, ftol=1e-9, max_nfev=max_nfev)
    return list(sol.x), _rms(sol.fun)


def fit_model(assignments, corrected, manifest: Manifest, anchor_mode: str, aniso: bool,
              candidates=None, do_loo=True):
    """Fit and compare candidate mixing models on the (anchored) pairs.

    Anisotropic keeps the exploratory additive (ℓx, ℓz) fit. Otherwise every named
    candidate is fit on all pairs (in-sample RMS) and, with do_loo, scored by
    leave-one-pair-out cross-validation (fit on 5, evaluate the held-out pair, all
    6 rotations, mean held-out RMS) — the primary ranking, robust on 6 pairs. Per-
    pair residuals under each fit, worst pair, parameter count and bound hits are
    reported; the winner is the lowest LOO RMS (ties -> fewer parameters)."""
    from scipy.optimize import least_squares

    geom = manifest.geom
    items = _pair_fit_inputs(assignments, corrected, manifest, anchor_mode)
    if not items:
        return {"error": "no fittable pairs"}

    if aniso:  # additive (ℓx, ℓz), γ=1 — unchanged exploratory mode
        def aniso_resid(p):
            r = [((model_section_curve(it["A"], it["B"], it["tdA"], it["tdB"], it["cov"], geom, p[0], p[1])
                   - it["target"]) * it["w"][None, :]).ravel() for it in items]
            return np.concatenate(r)

        sol = least_squares(aniso_resid, [0.3, 0.3], bounds=([1e-3, 1e-3], [5.0, 5.0]), xtol=1e-10, ftol=1e-10)
        return {"anchor": anchor_mode, "aniso": True,
                "global_fit": {"gamma": 1.0, "ell_x_mm": float(sol.x[0]), "ell_z_mm": float(sol.x[1]),
                               "residual_rms": _rms(sol.fun)}}

    names = candidates or [c["name"] for c in MODEL_CANDIDATES]
    cand_by_name = {c["name"]: c for c in MODEL_CANDIDATES}
    results = {}
    fitted_params = {}
    # Iterate in registry order so a warm_from base is fit before its dependent.
    for cand in MODEL_CANDIDATES:
        name = cand["name"]
        if name not in names:
            continue
        warm = None
        wf = cand.get("warm_from")
        if wf is not None and wf[0] in fitted_params:
            warm = wf[1](fitted_params[wf[0]])
        params, rms = _fit_cand(items, geom, cand, warm=warm)
        fitted_params[name] = params
        per_pair = {}
        for it in items:
            per_pair["+".join(it["pair"])] = _rms(_cand_resid([it], geom, cand, params))
        worst = max(per_pair.items(), key=lambda kv: kv[1])
        bounds_hit = {pn: bool(abs(v - lo) < 1e-6 or abs(v - hi) < 1e-4)
                      for pn, v, lo, hi in zip(cand["pnames"], params, cand["lo"], cand["hi"])}
        entry = {
            "params": {pn: round(float(v), 5) for pn, v in zip(cand["pnames"], params)},
            "n_params": len(cand["pnames"]),
            "in_sample_rms": round(rms, 5),
            "per_pair_rms": {k: round(v, 5) for k, v in per_pair.items()},
            "worst_pair": [worst[0], round(worst[1], 5)],
            "bounds_hit": bounds_hit,
        }
        if do_loo and len(items) > 2:
            held = []
            for k in range(len(items)):
                train = [it for j, it in enumerate(items) if j != k]
                test = [items[k]]
                # Warm-start each fold from the full-data fit (deterministic); a
                # capped polish keeps 36 refits tractable.
                p_tr, _ = _fit_cand(train, geom, cand, warm=params, max_nfev=40)
                held.append(_rms(_cand_resid(test, geom, cand, p_tr)))
            entry["loo_rms"] = round(float(np.mean(held)), 5)
            entry["loo_worst"] = round(float(np.max(held)), 5)
        results[name] = entry

    # Winner: parsimony rule on the LOO RMS (the primary criterion). Among all
    # candidates within a small margin of the best held-out RMS, take the FEWEST
    # parameters (ties -> lower LOO). A 2-param model that generalizes as well as a
    # 4-param one is preferred — the extra freedom is overfitting, not physics.
    winner = None
    winner_margin = 0.006
    if results:
        def loo_of(n):
            return results[n].get("loo_rms", results[n]["in_sample_rms"])
        best_loo = min(loo_of(n) for n in results)
        within = [n for n in results if loo_of(n) <= best_loo + winner_margin]
        winner = min(within, key=lambda n: (results[n]["n_params"], loo_of(n)))

    # Diagnostic: is any pair's mid darker than the linear mix of its (anchored)
    # endpoints? If not, candidate 3's micro-shadowing has nothing to explain.
    below_mix = {}
    for it in items:
        cov = it["cov"][:, None]
        lin_mix = (1 - cov) * it["A"][None, :] + cov * it["B"][None, :]
        d = (it["target"] - lin_mix)[1:SECTIONS - 1]  # interior sections
        below_mix["+".join(it["pair"])] = round(float(d.min()), 4)  # most-negative = darkest below mix

    return {
        "anchor": anchor_mode, "aniso": False,
        "candidates": results, "winner": winner,
        "winner_rule": f"fewest params within {winner_margin} LOO-RMS of the best",
        "min_interior_below_linmix": below_mix,
        "dropped_channels": {"+".join(it["pair"]): [bool(x) for x in (it["w"] == 0)] for it in items},
    }


# ---------------------------------------------------------------------------
# Plots + output.
# ---------------------------------------------------------------------------


def save_plots(assignments, corrected, manifest, fit, anchor_mode, out_dir):
    """Measured (points) vs modeled (lines) per pair, in the fit's space: the
    manufacturer-anchored measured curve vs the WINNING candidate's model, in
    linear RGB. Dropped (low-contrast) channels are drawn faded."""
    import os

    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    items = _pair_fit_inputs(assignments, corrected, manifest, anchor_mode)
    if not items:
        return None

    if fit.get("aniso"):
        gf = fit["global_fit"]
        kw = dict(ell_x=gf["ell_x_mm"], ell_z=gf["ell_z_mm"])
        label = f"aniso γ=1 ℓx={gf['ell_x_mm']:.3f} ℓz={gf['ell_z_mm']:.3f}"
    else:
        winner = fit.get("winner")
        cand = next(c for c in MODEL_CANDIDATES if c["name"] == winner)
        e = fit["candidates"][winner]
        params = [e["params"][pn] for pn in cand["pnames"]]
        kw = cand["kw"](params)
        label = f"{winner}: " + ", ".join(f"{pn}={v:g}" for pn, v in e["params"].items())

    ncol = 3
    nrow = int(math.ceil(len(items) / ncol))
    fig, axes = plt.subplots(nrow, ncol, figsize=(4 * ncol, 3 * nrow), squeeze=False)
    xs = np.linspace(0, 1, SECTIONS)
    for idx, it in enumerate(items):
        ax = axes[idx // ncol][idx % ncol]
        modeled = model_section_curve(it["A"], it["B"], it["tdA"], it["tdB"], it["cov"], manifest.geom, **kw)
        for ch, cname in zip(range(3), ("r", "g", "b")):
            alpha = 1.0 if it["w"][ch] > 0 else 0.25
            ax.plot(xs, it["target"][:, ch], "o", color=cname, ms=4, alpha=alpha)
            ax.plot(xs, modeled[:, ch], "-", color=cname, lw=1, alpha=alpha)
        ax.set_title("+".join(it["pair"]), fontsize=9)
        ax.set_xlabel("coverage (B fraction)")
        ax.set_ylabel("linear (anchored)" if anchor_mode == "mfg" else "linear")
        ax.set_ylim(0, max(1.05, float(np.max([it["A"], it["B"]])) + 0.05))
    for j in range(len(items), nrow * ncol):
        axes[j // ncol][j % ncol].axis("off")
    fig.suptitle(f"Measured (o) vs modeled (—) [{anchor_mode}-anchored] — {label}")
    fig.tight_layout()
    path = os.path.join(out_dir, "ramp_fits.png")
    fig.savefig(path, dpi=110)
    plt.close(fig)
    return path


def run_analysis(photo_path: str, manifest_path: str, out_dir: str, aniso: bool,
                 anchor_mode: str = "mfg", candidates=None, do_loo=True) -> dict:
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
    result: dict = {"photo": photo_path, "manifest": manifest_path,
                    "anchor_mode": anchor_mode, "n_plates_detected": len(plates)}
    if len(plates) == 0:
        result["error"] = "no plates detected"
        return result

    assignments, cluster_lin = identify_and_orient(plates, manifest)
    n_ident = sum(1 for a in assignments if a is not None)
    result["n_plates_identified"] = n_ident

    corrected = corrected_filaments(assignments, manifest)
    fit = fit_model(assignments, corrected, manifest, anchor_mode, aniso, candidates=candidates, do_loo=do_loo)
    result["fit"] = fit

    # Manufacturer hexes are the trusted endpoint truth. The photo-absolute
    # endpoints are kept only as a diagnostic — they are paper-anchored (assume
    # paper == white) and distorted by the phone's WB/tone mapping, so they are
    # NOT trusted as color truth; the anchored fit uses the manufacturer hexes.
    result["photo_absolute_endpoints"] = {
        "_note": "paper-anchored, WB/tone-distorted — diagnostic only, NOT trusted color truth",
        **{
            lbl: {
                "hex": d["hex"],
                "linear": [round(float(x), 4) for x in d["linear"]],
                "spread_srgb": round(d["spread_srgb"], 1),
                "n_measurements": d["n"],
                "clipped_channels": d["clipped"],
                "per_plate": d["per_plate"],
            }
            for lbl, d in corrected.items()
        },
    }
    result["manufacturer_hexes"] = {lbl: f.hex for lbl, f in manifest.filaments.items()}
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

    plot_path = save_plots(assignments, corrected, manifest, fit, anchor_mode, out_dir)
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


def generate_synthetic(manifest: Manifest, seed: int, model_kw: dict):
    """Return (photo_srgb_uint8, truth). The plates are rendered from the
    MANUFACTURER palette colors (the anchored fit's endpoint truth) through the
    given mixing model (model_kw passed to model_section_curve, e.g. {ell_x, gamma}
    or {ell_x, kappa}), then each plate gets a per-channel affine distortion (gain
    + offset) standing in for the phone's white balance, flare and per-plate black
    gloss. Manufacturer anchoring inverts that affine exactly, so the fit must
    recover the injected model parameters despite it."""
    import cv2

    rng = np.random.default_rng(seed)
    geom = manifest.geom
    labels = list(manifest.filaments.keys())
    # True endpoint colors ARE the manufacturer hexes (anchoring targets them).
    true_lin = {l: hex_to_linear(manifest.filaments[l].hex) for l in labels}
    # Paper is a mid off-white DARKER than the Cold White plate, so Cold White is
    # brighter than paper and normalizes to >1.0 (the real-photo regime).
    paper_lin = np.array([0.58, 0.55, 0.52])

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
        sec = model_section_curve(cA, cB, tdA, tdB, spec.realized, geom, **model_kw)
        # Per-plate, per-channel affine distortion (WB gain + flare/gloss offset).
        gain = rng.uniform(0.9, 1.1, size=3)
        offset = rng.uniform(-0.03, 0.03, size=3)
        sec = sec * gain[None, :] + offset[None, :]
        cA_disp = cA * gain + offset  # chamfer border under the same distortion
        plate = _render_plate_srgb(sec, cA_disp)  # linear, 100x900
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
    truth = {"model_kw": model_kw, "colors_lin": true_lin, "paper_lin": paper_lin, "placements": placements}
    return photo, truth


def _additive_reference_curve(cA, cB, tdA, tdB, cov, geom, ell):
    """Independent re-implementation of the additive (γ=1) forward model, used to
    guard that the γ machinery leaves γ=1 unchanged. Two linear Jacobi passes,
    self term always C0, w = weight × max(area_j, ε), 8-connected 1.0/0.1."""
    pattern = build_pattern(cov, geom)
    rows, cols = pattern.shape
    c0 = np.where(pattern[..., None] == 1, cB[None, None, :], cA[None, None, :]).astype(np.float64)
    td = np.where(pattern == 1, tdB, tdA).astype(np.float64)
    beta = neighbor_leak_grid(td, ell)
    area = np.maximum(np.broadcast_to((geom.row_heights() * geom.block_width_mm)[:, None], (rows, cols)), 1e-6)
    cur = c0.copy()
    for _ in range(BLEED_ITERATIONS):
        wsum = np.zeros((rows, cols))
        ssum = np.zeros((rows, cols, 3))
        for dr, dc, w in _OFFSETS:
            nv, valid = _shift(cur, dr, dc)
            na, _ = _shift(area, dr, dc)
            wij = w * na * valid
            wsum += wij
            ssum += wij[..., None] * nv
        nz = wsum > 0
        mean = np.zeros_like(ssum)
        mean[nz] = ssum[nz] / wsum[nz][..., None]
        nxt = np.where(((wsum > 0) & (beta > 0))[..., None], (1 - beta)[..., None] * c0 + beta[..., None] * mean, c0)
        cur = nxt
    return section_colors_from_grid(cur, geom)


def run_selftest(aniso: bool, ell_true: float, gamma_true: float, seed: int) -> int:
    import os
    import tempfile

    import cv2

    geom = Geom(
        block_width_mm=0.5263157894736842,
        row_height0_mm=0.20000000298023224,
        row_height_up_mm=0.07999999821186066,
        row_count=124,
    )
    ok = True

    def check(name, cond, detail=""):
        nonlocal ok
        status = "PASS" if cond else "FAIL"
        if not cond:
            ok = False
        print(f"  [{status}] {name} {detail}")

    # Regression guard: the γ=1 path must reproduce the additive forward model
    # bit-for-bit (independent reference), on a fixed input.
    cov = np.array([s / 8.0 for s in range(SECTIONS)])
    rcA, rcB = np.array([0.02, 0.03, 0.04]), np.array([0.9, 0.2, 0.01])
    g1 = model_section_curve(rcA, rcB, 0.1, 3.3, cov, geom, 0.25, None, gamma=1.0)
    ref = _additive_reference_curve(rcA, rcB, 0.1, 3.3, cov, geom, 0.25)
    check("γ=1 reproduces additive model (regression)", float(np.abs(g1 - ref).max()) < 1e-12,
          f"(max|Δ|={np.abs(g1 - ref).max():.2e})")

    manifest = _synthetic_manifest(geom)
    tmp = tempfile.mkdtemp(prefix="swatchphoto_selftest_")
    manifest_path = os.path.join(tmp, "manifest.swatch.json")
    _write_manifest_json(manifest, manifest_path)

    def run_scene(tag, model_kw, cands, aniso_flag):
        photo, truth = generate_synthetic(manifest, seed, model_kw)
        pp = os.path.join(tmp, tag + ".png")
        cv2.imwrite(pp, cv2.cvtColor(photo, cv2.COLOR_RGB2BGR))
        res = run_analysis(pp, manifest_path, os.path.join(tmp, tag), aniso_flag, "mfg",
                           candidates=cands, do_loo=False)
        return res, truth

    if aniso:
        res, truth = run_scene("aniso", {"ell_x": ell_true, "ell_z": ell_true}, None, True)
        check("detected 6 plates", res["n_plates_detected"] == 6, f"(got {res['n_plates_detected']})")
        check("identified 6 plates", res.get("n_plates_identified") == 6, f"(got {res.get('n_plates_identified')})")
        gf = res["fit"]["global_fit"]
        lx, lz = gf["ell_x_mm"], gf["ell_z_mm"]
        check("ℓx,ℓz recovery within 0.08mm", max(abs(lx - ell_true), abs(lz - ell_true)) < 0.08,
              f"(ℓx={lx:.3f} ℓz={lz:.3f}, true {ell_true})")
    else:
        # Candidate 1 / global-γ recovery + anchoring inversion + >1 carried.
        res, truth = run_scene("gamma", {"ell_x": ell_true, "gamma": gamma_true},
                               ["additive", "global_gamma"], False)
        check("detected 6 plates", res["n_plates_detected"] == 6, f"(got {res['n_plates_detected']})")
        check("identified 6 plates", res.get("n_plates_identified") == 6, f"(got {res.get('n_plates_identified')})")
        paper = truth["paper_lin"]
        any_over_one = any(bool((truth["colors_lin"][lbl] / paper > 1.02).any())
                           for lbl in res.get("manufacturer_hexes", {}))
        check("a plate is brighter than paper (>1.0 carried)", any_over_one, "(Cold White > 1.0)")
        cands = res["fit"]["candidates"]
        gg = cands["global_gamma"]["params"]
        check("γ recovery within 0.08", abs(gg["gamma"] - gamma_true) < 0.08,
              f"(γ={gg['gamma']:.3f}, true {gamma_true})")
        check("ℓ recovery within 0.06mm", abs(gg["ell"] - ell_true) < 0.06,
              f"(ℓ={gg['ell']:.4f}, true {ell_true})")
        check("subtractive (γ) in-sample <= additive",
              cands["global_gamma"]["in_sample_rms"] <= cands["additive"]["in_sample_rms"] + 1e-6,
              f"(γ rms={cands['global_gamma']['in_sample_rms']:.4f} vs additive {cands['additive']['in_sample_rms']:.4f})")

        # Candidate 2 (transmittance) recovery from a κ-generated scene.
        kap_true, ellk = 1.5, 0.3
        res2, _ = run_scene("kappa", {"ell_x": ellk, "kappa": kap_true},
                            ["additive", "transmittance"], False)
        check("κ-scene identified 6 plates", res2.get("n_plates_identified") == 6,
              f"(got {res2.get('n_plates_identified')})")
        tp = res2["fit"]["candidates"]["transmittance"]["params"]
        check("κ recovery within 0.3", abs(tp["kappa"] - kap_true) < 0.3, f"(κ={tp['kappa']:.3f}, true {kap_true})")
        check("ℓ(κ) recovery within 0.08mm", abs(tp["ell"] - ellk) < 0.08, f"(ℓ={tp['ell']:.4f}, true {ellk})")

    print(f"[selftest] {'ALL PASS' if ok else 'FAILURES'} — dir {tmp}")
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
    ap.add_argument("--anchor", choices=["mfg", "photo"], default="mfg",
                    help="endpoint truth: 'mfg' (default) anchors each plate's measured "
                         "endpoints to the manufacturer palette hexes and fits only the "
                         "relative mixing shape; 'photo' trusts the photo-absolute colors")
    ap.add_argument("--anisotropic", action="store_true", help="fit separate ℓx (X) and ℓz (Z) (γ=1)")
    ap.add_argument("--selftest", action="store_true", help="run the synthetic round-trip test")
    ap.add_argument("--ell", type=float, default=0.35, help="injected ℓ for --selftest")
    ap.add_argument("--gamma", type=float, default=0.4, help="injected γ for --selftest")
    ap.add_argument("--seed", type=int, default=0, help="RNG seed for --selftest")
    args = ap.parse_args(argv)

    if args.selftest:
        return run_selftest(args.anisotropic, args.ell, args.gamma, args.seed)

    if not args.photo or not args.manifest:
        ap.error("photo and manifest are required (or use --selftest)")

    import os

    out_dir = args.out or os.path.join(os.path.dirname(os.path.abspath(args.photo)), "swatchphoto_out")
    res = run_analysis(args.photo, args.manifest, out_dir, args.anisotropic, args.anchor)
    print(json.dumps({k: v for k, v in res.items() if not k.startswith("_")}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
