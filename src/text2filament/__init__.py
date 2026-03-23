import argparse

from .loader import load_glb
from .subdivide import subdivide
from .sample import sample_face_colors
from .palette import parse_palette, assign_palette
from .export_3mf import export_3mf
from .preview import export_preview


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="text2filament",
        description="Convert a textured GLB model to a multi-material 3MF file.",
    )
    parser.add_argument("input", help="Input .glb file")
    parser.add_argument(
        "--palette",
        default="#FF0000,#00FF00,#0000FF,#FFFFFF",
        help='Comma-separated hex colors (default: red,green,blue,white)',
    )
    parser.add_argument(
        "--resolution",
        type=float,
        default=0.025,
        metavar="UNITS",
        help="Target max edge length in model units (default: 0.025)",
    )
    parser.add_argument("--output", required=True, help="Output .3mf file")
    parser.add_argument(
        "--color-space",
        choices=["cielab", "rgb"],
        default="cielab",
        help="Color distance metric (default: cielab)",
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

    palette_hex = [c.strip() for c in args.palette.split(",")]
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

    print(f"Subdividing to {args.resolution:.4g} max edge length...")
    model = subdivide(model, args.resolution)
    print(f"  {len(model.mesh.vertices)} vertices, {len(model.mesh.faces)} faces after subdivision")

    print("Sampling texture colors...")
    face_colors = sample_face_colors(model)

    print("Matching palette...")
    assignments = assign_palette(face_colors, palette_rgb, args.color_space)

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
    export_3mf(model, assignments, args.output)
    print("Done.")
