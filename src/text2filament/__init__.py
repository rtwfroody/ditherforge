import argparse
import os

from .loader import load_glb
from .subdivide import subdivide, TooManyVerticesError
from .sample import sample_face_colors, sample_face_indices
from .palette import parse_palette, assign_palette, compute_palette
from .export_3mf import export_3mf, MAX_FILAMENTS
from .export_obj import export_obj
from .preview import export_preview


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="text2filament",
        description="Convert a textured GLB model to a multi-material 3MF file.",
    )
    parser.add_argument("input", help="Input .glb file")
    palette_group = parser.add_mutually_exclusive_group()
    palette_group.add_argument(
        "--palette",
        default="white,red,green,blue",
        help='Comma-separated colors — CSS names or hex (default: white,red,green,blue)',
    )
    palette_group.add_argument(
        "--auto-palette",
        type=int,
        metavar="N",
        help="Compute N dominant colors from the texture via k-means (mutually exclusive with --palette)",
    )
    parser.add_argument(
        "--resolution",
        type=float,
        default=0.5,
        metavar="MM",
        help="Target max edge length in mm after scaling (default: 0.5)",
    )
    parser.add_argument(
        "--glb-unit",
        choices=["m", "dm", "cm", "mm"],
        default="m",
        help="Unit of GLB coordinates (default: m). Converted to mm on load.",
    )
    parser.add_argument("--output", default="output.3mf",
                        help="Output file (.obj or .3mf, default: output.3mf)")
    parser.add_argument(
        "--color-space",
        choices=["cielab", "rgb"],
        default="cielab",
        help="Color distance metric (default: cielab)",
    )
    parser.add_argument(
        "--no-dither",
        dest="dither",
        action="store_false",
        help="Disable Floyd-Steinberg dithering (dithering is on by default)",
    )
    parser.set_defaults(dither=True)
    parser.add_argument(
        "--debug-textures",
        action="store_true",
        help="Save the original and dithered textures as PNGs next to the output file",
    )
    parser.add_argument(
        "--preview",
        action="store_true",
        help="Export a colored PLY alongside the 3MF for visual inspection",
    )
    parser.add_argument(
        "--stats",
        action="store_true",
        help="Print face count per material and mesh info",
    )
    args = parser.parse_args()

    output_ext = os.path.splitext(args.output)[1].lower()
    if output_ext not in (".obj", ".3mf"):
        print(f"Error: output must be .obj or .3mf, got {output_ext!r}")
        raise SystemExit(1)

    scale = {"m": 1000.0, "dm": 100.0, "cm": 10.0, "mm": 1.0}[args.glb_unit]
    print(f"Loading {args.input}...")
    model = load_glb(args.input, scale=scale)
    extent = model.mesh.vertices.max(axis=0) - model.mesh.vertices.min(axis=0)
    print(
        f"  {len(model.mesh.vertices)} vertices, "
        f"{len(model.mesh.faces)} faces, "
        f"texture {model.texture.size[0]}x{model.texture.size[1]} {model.texture.mode}, "
        f"extent {extent[0]:.3g} x {extent[1]:.3g} x {extent[2]:.3g}"
    )

    if args.auto_palette:
        n = args.auto_palette
        print(f"Computing {n}-color palette from texture...")
        palette_rgb = compute_palette(model.textures, n)
        hex_strs = [f"#{r:02X}{g:02X}{b:02X}" for r, g, b in palette_rgb]
        print(f"  Palette: {','.join(hex_strs)}")
    else:
        palette_hex = [c.strip() for c in args.palette.split(",")]
        palette_rgb = parse_palette(palette_hex)

    if output_ext == ".3mf" and len(palette_rgb) > MAX_FILAMENTS:
        print(f"Error: 3MF palette has {len(palette_rgb)} colors but max supported is {MAX_FILAMENTS}")
        raise SystemExit(1)

    base = os.path.splitext(args.output)[0]

    if args.debug_textures:
        for i, tex in enumerate(model.textures):
            suffix = f"_texture{i}.png" if len(model.textures) > 1 else "_texture.png"
            tex_path = base + suffix
            tex.save(tex_path)
            print(f"  Saved original texture → {tex_path}")

    MAX_VERTICES = 1_000_000
    resolution = args.resolution
    while True:
        print(f"Subdividing to {resolution:.4g} mm max edge length...")
        try:
            subdivided = subdivide(model, resolution, max_vertices=MAX_VERTICES)
        except TooManyVerticesError:
            resolution *= 1.5
            print(f"  Would exceed {MAX_VERTICES:,} vertices; retrying with resolution {resolution:.4g} mm...")
            continue
        model = subdivided
        print(f"  {len(model.mesh.vertices):,} vertices, {len(model.mesh.faces):,} faces after subdivision")
        break

    if args.dither:
        print("Sampling texture colors (Floyd-Steinberg dither)...")
        dithered_path = (base + "_dithered.png") if args.debug_textures else None
        assignments = sample_face_indices(model, palette_rgb, save_path=dithered_path)
        if dithered_path:
            print(f"  Saved dithered texture → {dithered_path}")
    else:
        print("Sampling texture colors...")
        face_colors = sample_face_colors(model)
        print("Matching palette...")
        assignments = assign_palette(face_colors, palette_rgb, args.color_space)

    if model.no_texture_mask is not None:
        assignments[model.no_texture_mask] = 0

    if args.stats:
        print("  Face counts per material:")
        for i, (r, g, b) in enumerate(palette_rgb):
            hex_color = f"#{r:02X}{g:02X}{b:02X}"
            count = int((assignments == i).sum())
            print(f"    [{i}] {hex_color}: {count} faces")

    if args.preview:
        preview_path = args.output.replace(".3mf", "_preview.ply")
        print(f"Writing preview to {preview_path}...")
        export_preview(model, assignments, palette_rgb, preview_path)

    print(f"Exporting {args.output}...")
    if output_ext == ".3mf":
        export_3mf(model, assignments, args.output)
    else:
        export_obj(model, assignments, palette_rgb, args.output)
    print("Done.")
