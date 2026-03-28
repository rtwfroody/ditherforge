#!/usr/bin/env python3
"""Reduce texture resolution in a GLB file to shrink its size.

Downscales all embedded textures to at most --max-size pixels on their
longest side, and re-encodes them as JPEG at the given --quality.

Usage: reduce_texture.py input.glb output.glb [--max-size 512] [--quality 80]

Requires: trimesh, Pillow (install with uv)
"""

import argparse
import io

import trimesh
from PIL import Image


def reduce_textures(scene, max_size, quality):
    """Downscale and re-encode all textures in the scene."""
    seen = set()
    geometries = scene.geometry.items() if isinstance(scene, trimesh.Scene) else []

    for name, geom in geometries:
        if not isinstance(geom, trimesh.Trimesh):
            continue
        visual = geom.visual
        if not hasattr(visual, 'material') or visual.material is None:
            continue
        mat = visual.material

        # PBRMaterial stores the image on baseColorTexture or image
        for attr in ('baseColorTexture', 'image'):
            img = getattr(mat, attr, None)
            if img is None or not isinstance(img, Image.Image):
                continue
            img_id = id(img)
            if img_id in seen:
                continue
            seen.add(img_id)

            orig_size = img.size
            # Downscale if larger than max_size
            scale = min(max_size / max(img.size), 1.0)
            if scale < 1.0:
                new_size = (int(img.width * scale), int(img.height * scale))
                img = img.resize(new_size, Image.LANCZOS)
                setattr(mat, attr, img)

            # Estimate compressed size
            buf = io.BytesIO()
            img.convert('RGB').save(buf, format='JPEG', quality=quality)
            kb = buf.tell() / 1024
            print(f"  {name}/{attr}: {orig_size} -> {img.size} ({kb:.0f} KB)")


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('input', help='Input GLB file')
    parser.add_argument('output', help='Output GLB file')
    parser.add_argument('--max-size', type=int, default=512,
                        help='Max texture dimension in pixels (default: 512)')
    parser.add_argument('--quality', type=int, default=80,
                        help='JPEG quality 1-100 (default: 80)')
    args = parser.parse_args()

    print(f"Loading {args.input}...")
    scene = trimesh.load(args.input)

    print("Reducing textures...")
    reduce_textures(scene, args.max_size, args.quality)

    print(f"Exporting {args.output}...")
    scene.export(args.output)

    # Report sizes
    import os
    in_size = os.path.getsize(args.input) / 1024
    out_size = os.path.getsize(args.output) / 1024
    print(f"  {in_size:.0f} KB -> {out_size:.0f} KB ({out_size/in_size*100:.0f}%)")


if __name__ == '__main__':
    main()
