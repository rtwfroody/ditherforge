import argparse
import os

from .loader import load_glb
from .subdivide import subdivide
from .sample import sample_face_colors, sample_face_indices
from .palette import parse_palette, assign_palette
from .export_3mf import export_3mf, MAX_FILAMENTS
from .export_obj import export_obj
from .preview import export_preview


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="text2filament",
        description="Convert a textured GLB model to a multi-material 3MF file.",
    )
    parser.add_argument("input", help="Input .glb file")
    parser.add_argument(
        "--palette",
        default="white,red,green,blue",
        help='Comma-separated colors — CSS names or hex, max 3 for .3mf (default: white,red,green,blue)',
    )
    parser.add_argument(
        "--resolution",
        type=float,
        default=0.025,
        metavar="UNITS",
        help="Target max edge length in model units (default: 0.025)",
    )
    parser.add_argument("--output", default="output.obj",
                        help="Output file (.obj or .3mf, default: output.obj)")
    parser.add_argument(
        "--color-space",
        choices=["cielab", "rgb"],
        default="cielab",
        help="Color distance metric (default: cielab)",
    )
    parser.add_argument(
        "--dither",
        action="store_true",
        help="Apply Floyd-Steinberg dithering in texture space before palette assignment",
    )
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

    palette_hex = [c.strip() for c in args.palette.split(",")]
    if output_ext == ".3mf" and len(palette_hex) > MAX_FILAMENTS:
        print(f"Error: 3MF palette has {len(palette_hex)} colors but max supported is {MAX_FILAMENTS}")
        raise SystemExit(1)
    palette_rgb = parse_palette(palette_hex)

    print(f"Loading {args.input}...")
    model = load_glb(args.input)
    extent = model.mesh.vertices.max(axis=0) - model.mesh.vertices.min(axis=0)
    print(
        f"  {len(model.mesh.vertices)} vertices, "
        f"{len(model.mesh.faces)} faces, "
        f"texture {model.texture.size[0]}x{model.texture.size[1]} {model.texture.mode}, "
        f"extent {extent[0]:.3g} x {extent[1]:.3g} x {extent[2]:.3g}"
    )

    base = os.path.splitext(args.output)[0]

    if args.debug_textures:
        tex_path = base + "_texture.png"
        model.texture.save(tex_path)
        print(f"  Saved original texture → {tex_path}")

    print(f"Subdividing to {args.resolution:.4g} max edge length...")
    model = subdivide(model, args.resolution)
    print(f"  {len(model.mesh.vertices)} vertices, {len(model.mesh.faces)} faces after subdivision")

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
        for i, hex_color in enumerate(palette_hex):
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
