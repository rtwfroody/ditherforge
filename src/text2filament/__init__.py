import argparse
import sys
from .loader import load_glb


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="text2filament",
        description="Convert a textured GLB model to a multi-material 3MF file.",
    )
    parser.add_argument("input", help="Input .glb file")
    parser.add_argument(
        "--palette",
        required=True,
        help='Comma-separated hex colors, e.g. "#FFFFFF,#000000,#CC0000"',
    )
    parser.add_argument(
        "--resolution",
        type=float,
        default=1.0,
        metavar="MM",
        help="Target max edge length in mm (default: 1.0)",
    )
    parser.add_argument("--output", required=True, help="Output .3mf file")
    parser.add_argument(
        "--color-space",
        choices=["cielab", "rgb"],
        default="cielab",
        help="Color distance metric (default: cielab)",
    )
    parser.add_argument(
        "--stats",
        action="store_true",
        help="Print face count per material and mesh info",
    )
    args = parser.parse_args()

    palette = [c.strip() for c in args.palette.split(",")]

    print(f"Loading {args.input}...")
    model = load_glb(args.input)
    print(
        f"  {len(model.mesh.vertices)} vertices, "
        f"{len(model.mesh.faces)} faces, "
        f"texture {model.texture.size[0]}x{model.texture.size[1]} {model.texture.mode}"
    )
    print(f"  Palette: {palette}")
    print(f"  Resolution: {args.resolution}mm")
    print("(Pipeline not yet implemented)")
    sys.exit(0)
