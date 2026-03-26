#!/usr/bin/env python3
"""Fix common mesh issues in GLB/OBJ/STL files for 3D printing.

Uses PyMeshLab for robust repair (non-manifold edges/vertices, holes,
degenerate/duplicate faces) and trimesh for GLB I/O with texture
preservation.

Usage: fix_mesh.py input.glb [output.glb]
If output is omitted, overwrites the input file.

Requires: pymeshlab, trimesh, numpy (install in .venv with uv)
"""

import sys
import tempfile
import os

import pymeshlab
import trimesh


def repair_mesh(mesh):
    """Repair a single trimesh.Trimesh using PyMeshLab filters."""
    verts_before = len(mesh.vertices)
    faces_before = len(mesh.faces)

    # Export to a temp file for PyMeshLab (PLY preserves enough info).
    with tempfile.TemporaryDirectory() as td:
        tmp_in = os.path.join(td, "in.ply")
        tmp_out = os.path.join(td, "out.ply")
        mesh.export(tmp_in)

        ms = pymeshlab.MeshSet()
        ms.load_new_mesh(tmp_in)

        # Remove duplicate vertices and faces.
        ms.meshing_remove_duplicate_vertices()
        ms.meshing_remove_duplicate_faces()

        # Remove unreferenced vertices.
        ms.meshing_remove_unreferenced_vertices()

        # Repair non-manifold edges (remove or split faces).
        ms.meshing_repair_non_manifold_edges()

        # Repair non-manifold vertices (split into manifold components).
        ms.meshing_repair_non_manifold_vertices()

        # Recompute normals.
        ms.compute_normal_per_face()
        ms.compute_normal_per_vertex()

        # Close holes (max hole size in edge count).
        ms.meshing_close_holes(maxholesize=100)

        ms.save_current_mesh(tmp_out)

        # Load repaired geometry back.
        repaired = trimesh.load(tmp_out, process=False)

    # Transfer texture coordinates from original if the repaired mesh
    # has the same vertex count (repair didn't add/remove many vertices).
    # Otherwise the texture data may not align.
    if hasattr(mesh, 'visual') and hasattr(mesh.visual, 'uv') and mesh.visual.uv is not None:
        if len(repaired.vertices) == len(mesh.vertices):
            repaired.visual = mesh.visual
        else:
            # Vertices changed — try to map UVs by nearest vertex.
            from scipy.spatial import cKDTree
            tree = cKDTree(mesh.vertices)
            _, indices = tree.query(repaired.vertices)
            uv = mesh.visual.uv[indices]
            if hasattr(mesh.visual, 'material'):
                repaired.visual = trimesh.visual.TextureVisuals(
                    uv=uv, material=mesh.visual.material)

    verts_after = len(repaired.vertices)
    faces_after = len(repaired.faces)
    print(f"  {verts_before} verts, {faces_before} faces -> "
          f"{verts_after} verts, {faces_after} faces")
    print(f"  Watertight: {repaired.is_watertight}")

    return repaired


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <input> [output]")
        sys.exit(1)

    input_path = sys.argv[1]
    output_path = sys.argv[2] if len(sys.argv) > 2 else input_path

    scene = trimesh.load(input_path)

    if isinstance(scene, trimesh.Scene):
        meshes = list(scene.geometry.items())
        for name, geom in meshes:
            if isinstance(geom, trimesh.Trimesh):
                print(f"Repairing '{name}':")
                scene.geometry[name] = repair_mesh(geom)
        scene.export(output_path)
    else:
        print("Repairing mesh:")
        repaired = repair_mesh(scene)
        repaired.export(output_path)

    print(f"Saved to {output_path}")


if __name__ == "__main__":
    main()
