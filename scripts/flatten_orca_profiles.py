#!/usr/bin/env python3
"""Flatten OrcaSlicer machine + process profiles into single-level JSON files
so ditherforge can embed them without carrying OrcaSlicer's inheritance graph.

For each printer/nozzle combination we care about:
  * Walk the `inherits` chain from the leaf profile up to the root, merging
    settings along the way (leaf wins).
  * Strip OrcaSlicer bookkeeping keys (type, inherits, setting_id, from,
    instantiation, compatible_printers, setting_description).
  * Write the flattened JSON under internal/export3mf/profiles/<printer-id>/.

The script also emits manifest.json describing the available printers,
nozzles, and layer heights. The Go registry reads this manifest.

Per-process manifest entries also carry the actual voxel-relevant
slicer settings — the XY line widths and Z heights that determine
voxel cell dimensions:
  * `layer_height`           — Z height for upper layers.
  * `line_width`             — default extrusion width (XY voxel size for upper layers).
  * `initial_layer_print_height` — Z height for layer 0.
  * `initial_layer_line_width`   — extrusion width for layer 0 (XY voxel size for layer 0).

These replace ditherforge's previous approximations
(nozzle * 1.275 for first-layer XY, nozzle * 1.05 for upper XY) so
the voxel grid matches what the slicer will actually extrude.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
OUT_ROOT = REPO_ROOT / "internal" / "export3mf" / "profiles"

# Keys that are OrcaSlicer internals, not slicer settings.
STRIP_KEYS = {
    "type",
    "inherits",
    "setting_id",
    "from",
    "instantiation",
    "compatible_printers",
    "compatible_printers_condition",
    "compatible_prints",
    "compatible_prints_condition",
    "setting_description",
}


@dataclass
class PrinterSpec:
    """A printer we want to include, with the set of nozzle variants."""
    id: str               # ditherforge slug, e.g. "snapmaker_u1"
    display_name: str     # UI name, e.g. "Snapmaker U1"
    vendor_dir: str       # OrcaSlicer vendor folder name
    # Machine profile filename prefix — we match "<prefix> <nozzle> nozzle.json"
    # with flexible separators.
    machine_prefix: str
    # Process profiles match "@<printer_name_in_process>"; some printers use
    # a different string in process profile names than in machine filenames.
    process_marker: str   # e.g. "Snapmaker U1", "Prusa XL 5T", "BBL H2D"
    # Whether this is a Bambu Lab printer. Bambu Studio's 3MF importer switches
    # between a generic geometry-only path and a strict BBL-project path based
    # on the <metadata name="Application"> prefix; the exporter uses this flag
    # to pick which flavour of 3MF to emit.
    is_bambu: bool = False


# Printers that can handle multi-material efficiently without a big AMS tax.
PRINTERS: list[PrinterSpec] = [
    PrinterSpec(
        id="snapmaker_u1",
        display_name="Snapmaker U1",
        vendor_dir="Snapmaker",
        machine_prefix="Snapmaker U1",
        process_marker="Snapmaker U1",
    ),
    PrinterSpec(
        id="snapmaker_j1",
        display_name="Snapmaker J1",
        vendor_dir="Snapmaker",
        machine_prefix="Snapmaker J1",
        process_marker="Snapmaker J1",
    ),
    PrinterSpec(
        id="prusa_xl",
        display_name="Prusa XL (2 tools)",
        vendor_dir="Prusa",
        machine_prefix="Prusa XL",
        process_marker="Prusa XL",
    ),
    PrinterSpec(
        id="prusa_xl_5t",
        display_name="Prusa XL (5 tools)",
        vendor_dir="Prusa",
        machine_prefix="Prusa XL 5T",
        process_marker="Prusa XL 5T",
    ),
    PrinterSpec(
        id="bambu_h2d",
        display_name="Bambu Lab H2D",
        vendor_dir="BBL",
        machine_prefix="Bambu Lab H2D",
        process_marker="BBL H2D",
        is_bambu=True,
    ),
    PrinterSpec(
        id="bambu_h2d_pro",
        display_name="Bambu Lab H2D Pro",
        vendor_dir="BBL",
        machine_prefix="Bambu Lab H2D Pro",
        process_marker="BBL H2DP",
        is_bambu=True,
    ),
]


def load_profile(path: Path) -> dict:
    with path.open() as f:
        return json.load(f)


def build_index(vendor_dir: Path, subdir: str) -> dict[str, Path]:
    """Index all profile JSONs in vendor/subdir by their `name` field."""
    index: dict[str, Path] = {}
    root = vendor_dir / subdir
    if not root.is_dir():
        return index
    for p in sorted(root.glob("*.json")):
        try:
            data = load_profile(p)
        except Exception as exc:
            print(f"warning: failed to parse {p}: {exc}", file=sys.stderr)
            continue
        name = data.get("name")
        if isinstance(name, str):
            index[name] = p
    return index


def resolve(name: str, index: dict[str, Path], cache: dict[str, dict]) -> dict:
    """Return the flattened settings for a profile name by walking inherits."""
    if name in cache:
        return cache[name]
    if name not in index:
        raise KeyError(f"profile {name!r} not found in index")
    raw = load_profile(index[name])
    parent_name = raw.get("inherits")
    if parent_name:
        merged = dict(resolve(parent_name, index, cache))
    else:
        merged = {}
    for k, v in raw.items():
        merged[k] = v
    cache[name] = merged
    return merged


def strip_internals(d: dict) -> dict:
    return {k: v for k, v in d.items() if k not in STRIP_KEYS}


def parse_float_field(d: dict, key: str) -> float | None:
    """Parse a flattened-profile field as a float.

    OrcaSlicer stores most numeric settings as JSON strings (e.g.
    "0.42"). A handful are emitted as bare numbers, and per-extruder
    settings come through as a single-element list (e.g. ["0.4"]).
    Returns None when the value is missing, empty, or not parseable
    so the caller can fall back to a default.
    """
    v = d.get(key)
    if isinstance(v, list):
        # Per-extruder fields: take the first element. ditherforge is
        # single-extruder for voxel-grid purposes.
        if not v:
            return None
        v = v[0]
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, str):
        try:
            return float(v.strip())
        except ValueError:
            return None
    return None


MACHINE_PATTERN = re.compile(
    r"^(?P<prefix>.+?)\s*\(?\s*(?P<nozzle>\d+\.\d+)\s*(?:mm)?\s*nozzle\)?$",
    re.IGNORECASE,
)


def match_nozzle(machine_name: str, prefix: str) -> str | None:
    """Return the nozzle diameter (e.g. '0.4') if machine_name matches prefix."""
    m = MACHINE_PATTERN.match(machine_name.strip())
    if not m:
        return None
    # The prefix (printer family) must match exactly (case-insensitive, with
    # any amount of whitespace). Be strict so "Prusa XL" doesn't pick up
    # "Prusa XL 5T".
    got_prefix = m.group("prefix").strip().casefold()
    want_prefix = prefix.strip().casefold()
    if got_prefix != want_prefix:
        return None
    return m.group("nozzle")


PROCESS_PATTERN = re.compile(r"@\s*(?P<marker>.+?)\s*$")


def process_matches(process_name: str, marker: str) -> bool:
    # Process profile names look like "0.20 Standard @Snapmaker U1 (0.4 nozzle)"
    # or "0.16mm Speed @Prusa XL 0.4". The marker is what appears immediately
    # after the @, up to (and sometimes including) the nozzle qualifier.
    if "@" not in process_name:
        return False
    after_at = process_name.split("@", 1)[1].strip()
    # Match if marker is the start of the after-@ chunk. Use whole-word boundary.
    mm = marker.strip().casefold()
    aa = after_at.casefold()
    if not aa.startswith(mm):
        return False
    # Next character must not be a letter/digit (avoid "Prusa XL" matching
    # "Prusa XL 5T").
    nxt = aa[len(mm):len(mm) + 1]
    return nxt in ("", " ", "(")


def extract_nozzle_from_process(process_name: str) -> str | None:
    """Extract nozzle diameter from a process profile name, if present."""
    m = re.search(r"(\d+\.\d+)\s*(?:mm)?\s*nozzle", process_name, re.IGNORECASE)
    if m:
        return m.group(1)
    # Some Prusa process names look like "0.20mm Speed @Prusa XL 0.4"
    m2 = re.search(r"\s(\d+\.\d+)$", process_name)
    if m2:
        return m2.group(1)
    return None


# Filament used as the seed profile for BBL-project exports. Bambu Studio
# validates project_settings.config against a full PresetBundle, so we need a
# real flattened filament JSON to expand filament_* arrays from.
BAMBU_SEED_FILAMENT = "Bambu PLA Basic"


def find_filament_profile(
    filament_idx: dict[str, Path],
    base_name: str,
    marker: str,
    nozzle: str,
) -> str | None:
    """Return the filament profile name matching this printer+nozzle, or None.

    Tries "<base> @<marker> <nozzle> nozzle" first, then falls back to the
    nozzle-less "<base> @<marker>" (OrcaSlicer's 0.4mm default convention).
    """
    qualified = f"{base_name} @{marker} {nozzle} nozzle"
    if qualified in filament_idx:
        return qualified
    default = f"{base_name} @{marker}"
    if default in filament_idx:
        return default
    return None


def flatten_printer(spec: PrinterSpec, orca_root: Path) -> dict:
    """Flatten all profiles for one printer, return manifest entry.

    orca_root is the OrcaSlicer profiles directory (the one containing
    Snapmaker/, Prusa/, BBL/, …).
    """
    vendor = orca_root / spec.vendor_dir
    machine_idx = build_index(vendor, "machine")
    process_idx = build_index(vendor, "process")
    filament_idx = build_index(vendor, "filament") if spec.is_bambu else {}

    machine_cache: dict[str, dict] = {}
    process_cache: dict[str, dict] = {}
    filament_cache: dict[str, dict] = {}

    # Find machine profiles for this printer (one per nozzle variant).
    nozzle_to_machine: dict[str, dict] = {}
    nozzle_to_machine_name: dict[str, str] = {}
    for name in machine_idx:
        nozzle = match_nozzle(name, spec.machine_prefix)
        if not nozzle:
            continue
        # Only keep instantiable leaf profiles.
        raw = load_profile(machine_idx[name])
        if raw.get("instantiation") != "true":
            continue
        flat = resolve(name, machine_idx, machine_cache)
        nozzle_to_machine[nozzle] = flat
        nozzle_to_machine_name[nozzle] = name

    if not nozzle_to_machine:
        raise RuntimeError(f"no machine profiles matched for {spec.id!r} "
                           f"(prefix={spec.machine_prefix!r})")

    # Find process profiles for this printer.
    # Map: nozzle -> list of (layer_height, name, flattened)
    # Process profiles that don't mention a nozzle apply to the default
    # (0.4mm) nozzle for that printer (OrcaSlicer convention).
    nozzle_to_processes: dict[str, list[tuple[float, str, dict]]] = {
        n: [] for n in nozzle_to_machine
    }
    for name in process_idx:
        if not process_matches(name, spec.process_marker):
            continue
        # Skip specialty/benchmark profiles that aren't general-purpose.
        lname = name.lower()
        if "benchy" in lname:
            continue
        raw = load_profile(process_idx[name])
        if raw.get("instantiation") != "true":
            continue
        flat = resolve(name, process_idx, process_cache)
        lh_str = flat.get("layer_height")
        if not lh_str:
            continue
        try:
            lh = float(lh_str)
        except (TypeError, ValueError):
            continue
        proc_nozzle = extract_nozzle_from_process(name)
        if proc_nozzle is None:
            # OrcaSlicer convention: profile name without a nozzle qualifier
            # applies to the default 0.4mm nozzle for that printer.
            proc_nozzle = "0.4"
        if proc_nozzle not in nozzle_to_machine:
            continue
        nozzle_to_processes[proc_nozzle].append((lh, name, flat))

    # De-duplicate process profiles by layer height. OrcaSlicer often has
    # multiple named variants at the same layer height (e.g. "Quality",
    # "Strength", "Support"). We keep the one whose name contains "Standard"
    # or "Optimal" or sorts first alphabetically — a stable, conservative
    # default.
    def profile_priority(name: str) -> tuple:
        lname = name.lower()
        # Lower tuple = higher priority.
        if "standard" in lname:
            return (0, name)
        if "optimal" in lname:
            return (1, name)
        if "speed" in lname:
            return (2, name)
        if "fine" in lname or "extra fine" in lname:
            return (3, name)
        if "draft" in lname:
            return (4, name)
        if "strength" in lname or "support" in lname or "bambu" in lname or "benchy" in lname:
            return (9, name)  # deprioritise specialty profiles
        return (5, name)

    printer_dir = OUT_ROOT / spec.id
    printer_dir.mkdir(parents=True, exist_ok=True)

    manifest_nozzles: list[dict] = []
    for nozzle in sorted(nozzle_to_machine, key=lambda s: float(s)):
        machine_flat = strip_internals(nozzle_to_machine[nozzle])
        machine_name = nozzle_to_machine_name[nozzle]
        machine_path = printer_dir / f"machine_{nozzle}.json"
        with machine_path.open("w") as f:
            json.dump(machine_flat, f, indent=2, sort_keys=True)

        # Dedupe processes by layer height
        by_lh: dict[float, tuple[float, str, dict]] = {}
        for lh, name, flat in nozzle_to_processes[nozzle]:
            if lh in by_lh:
                old = by_lh[lh]
                if profile_priority(name) < profile_priority(old[1]):
                    by_lh[lh] = (lh, name, flat)
            else:
                by_lh[lh] = (lh, name, flat)

        process_entries: list[dict] = []
        for lh in sorted(by_lh.keys()):
            _, pname, pflat = by_lh[lh]
            pflat_clean = strip_internals(pflat)
            fname = f"process_{nozzle}_{lh:.2f}.json"
            with (printer_dir / fname).open("w") as f:
                json.dump(pflat_clean, f, indent=2, sort_keys=True)
            entry: dict = {
                "layer_height": lh,
                "name": pname,
                "file": fname,
            }
            # Voxel-grid sizes from the actual slicer settings. Missing or
            # unparseable values stay absent in the manifest and the Go
            # consumer falls back to the legacy nozzle*scale approximation.
            for key in (
                "line_width",
                "initial_layer_line_width",
                "initial_layer_print_height",
            ):
                v = parse_float_field(pflat, key)
                if v is not None:
                    entry[key] = v
            process_entries.append(entry)

        if not process_entries:
            print(f"warning: no process profiles for {spec.id} nozzle {nozzle}",
                  file=sys.stderr)

        nozzle_entry = {
            "diameter": nozzle,
            "printer_settings_id": machine_name,
            "machine_file": machine_path.name,
            "processes": process_entries,
        }

        if spec.is_bambu:
            filament_name = find_filament_profile(
                filament_idx, BAMBU_SEED_FILAMENT, spec.process_marker, nozzle)
            if filament_name is None:
                print(f"warning: no {BAMBU_SEED_FILAMENT} filament profile for "
                      f"{spec.id} nozzle {nozzle}", file=sys.stderr)
            else:
                filament_flat = resolve(filament_name, filament_idx, filament_cache)
                filament_clean = strip_internals(filament_flat)
                filament_fname = f"filament_{nozzle}.json"
                with (printer_dir / filament_fname).open("w") as f:
                    json.dump(filament_clean, f, indent=2, sort_keys=True)
                nozzle_entry["filament_file"] = filament_fname
                nozzle_entry["filament_settings_id"] = filament_name

        manifest_nozzles.append(nozzle_entry)

    entry = {
        "id": spec.id,
        "display_name": spec.display_name,
        "nozzles": manifest_nozzles,
    }
    if spec.is_bambu:
        entry["is_bambu"] = True
    return entry


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Flatten OrcaSlicer profiles into ditherforge's embedded registry.",
    )
    parser.add_argument(
        "orca_source",
        type=Path,
        help=("Path to a checked-out OrcaSlicer source tree "
              "(the directory containing resources/profiles/). "
              "Either the repo root or its resources/profiles subdir works; "
              "the script accepts both."),
    )
    args = parser.parse_args()

    src = args.orca_source.expanduser().resolve()
    # Accept either the repo root (.../OrcaSlicer) or the resources
    # tree (.../OrcaSlicer/resources/profiles) directly. Pick the
    # first existing layout; complain loudly if neither resolves.
    candidates = [src / "resources" / "profiles", src]
    orca_root: Path | None = None
    for c in candidates:
        if c.is_dir() and (c / "Snapmaker").is_dir():
            orca_root = c
            break
    if orca_root is None:
        sys.exit(
            f"error: {src} does not look like an OrcaSlicer source tree "
            f"(expected resources/profiles/Snapmaker or a profiles subdir under it)"
        )

    OUT_ROOT.mkdir(parents=True, exist_ok=True)
    manifest: list[dict] = []
    for spec in PRINTERS:
        print(f"Flattening {spec.id}...")
        manifest.append(flatten_printer(spec, orca_root))
    with (OUT_ROOT / "manifest.json").open("w") as f:
        json.dump({"printers": manifest}, f, indent=2)
    print(f"Wrote manifest with {len(manifest)} printers to {OUT_ROOT}/manifest.json")


if __name__ == "__main__":
    main()
