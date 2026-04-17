#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10,<3.13"
# dependencies = ["cgal>=6.0,<7"]
# ///
"""Alpha-wrap a triangle mesh using CGAL Alpha_wrap_3.

Reads a binary STL, runs alpha_wrap_3, writes a binary STL.

Invoked by the Go side with `uv run --script`; the PEP 723 header above
provisions an ephemeral environment on first use.
"""

import argparse
import struct
import sys


def read_stl(path):
    with open(path, "rb") as f:
        data = f.read()
    # Detect ASCII STL.
    if data[:5] == b"solid" and b"facet" in data[:1024]:
        return _read_ascii_stl(data)
    return _read_binary_stl(data)


def _read_binary_stl(data):
    ntri = struct.unpack_from("<I", data, 80)[0]
    tris = []
    off = 84
    for _ in range(ntri):
        # 12 bytes normal, 3*12 bytes verts, 2 bytes attr
        vs = struct.unpack_from("<9f", data, off + 12)
        tris.append(((vs[0], vs[1], vs[2]),
                     (vs[3], vs[4], vs[5]),
                     (vs[6], vs[7], vs[8])))
        off += 50
    return tris


def _read_ascii_stl(data):
    tris = []
    cur = []
    for line in data.decode("ascii", errors="replace").splitlines():
        line = line.strip()
        if line.startswith("vertex"):
            parts = line.split()
            cur.append((float(parts[1]), float(parts[2]), float(parts[3])))
            if len(cur) == 3:
                tris.append(tuple(cur))
                cur = []
    return tris


def soup_from_tris(tris):
    from CGAL.CGAL_Alpha_wrap_3 import Point_3_Vector, Polygon_Vector, Int_Vector
    from CGAL.CGAL_Kernel import Point_3

    # Deduplicate vertices so CGAL sees shared edges where possible.
    index = {}
    pts = Point_3_Vector()
    polys = Polygon_Vector()
    for tri in tris:
        iv = Int_Vector()
        for v in tri:
            k = (round(v[0], 6), round(v[1], 6), round(v[2], 6))
            idx = index.get(k)
            if idx is None:
                idx = len(index)
                index[k] = idx
                pts.append(Point_3(v[0], v[1], v[2]))
            iv.append(idx)
        polys.append(iv)
    return pts, polys


def polyhedron_to_stl(poly, path):
    # Extract triangles from the Polyhedron_3. Alpha-wrap output is
    # triangulated, so every facet has exactly 3 halfedges.
    tris = []
    for facet in poly.facets():
        h0 = facet.halfedge()
        h1 = h0.next()
        h2 = h1.next()
        p0 = h0.vertex().point()
        p1 = h1.vertex().point()
        p2 = h2.vertex().point()
        tris.append((
            (p0.x(), p0.y(), p0.z()),
            (p1.x(), p1.y(), p1.z()),
            (p2.x(), p2.y(), p2.z()),
        ))

    with open(path, "wb") as f:
        f.write(b"\x00" * 80)
        f.write(struct.pack("<I", len(tris)))
        for (a, b, c) in tris:
            # Normal (zeros - slicers recompute).
            f.write(struct.pack("<3f", 0.0, 0.0, 0.0))
            f.write(struct.pack("<3f", *a))
            f.write(struct.pack("<3f", *b))
            f.write(struct.pack("<3f", *c))
            f.write(b"\x00\x00")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="inp", required=True)
    ap.add_argument("--out", dest="out", required=True)
    ap.add_argument("--alpha", type=float, required=True)
    ap.add_argument("--offset", type=float, required=True)
    args = ap.parse_args()

    if args.alpha <= 0 or args.offset <= 0:
        print(f"alpha and offset must be positive (got {args.alpha}, {args.offset})",
              file=sys.stderr)
        sys.exit(2)

    tris = read_stl(args.inp)
    if not tris:
        print("input mesh has no triangles", file=sys.stderr)
        sys.exit(2)

    from CGAL.CGAL_Alpha_wrap_3 import alpha_wrap_3
    from CGAL.CGAL_Polyhedron_3 import Polyhedron_3

    pts, polys = soup_from_tris(tris)
    out = Polyhedron_3()
    alpha_wrap_3(pts, polys, args.alpha, args.offset, out)

    print(f"alpha_wrap: {out.size_of_vertices()} vertices, {out.size_of_facets()} facets",
          file=sys.stderr)

    polyhedron_to_stl(out, args.out)


if __name__ == "__main__":
    main()
