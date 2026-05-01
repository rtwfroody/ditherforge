// Boolean operations on closed triangle meshes via CGAL's
// Polygon_mesh_processing::corefine_and_compute_{union,difference}.
//
// Inputs are oriented through the same polygon-soup pipeline that
// cgalclip uses, so callers may pass triangle soups (we orient them).
// Failures (self-intersection, non-orientable input, non-closed mesh)
// surface as a CResult with .error set.

#include "cgalbool.h"
#include <CGAL/Exact_predicates_inexact_constructions_kernel.h>
#include <CGAL/Surface_mesh.h>
#include <CGAL/Polygon_mesh_processing/corefinement.h>
#include <CGAL/Polygon_mesh_processing/orient_polygon_soup.h>
#include <CGAL/Polygon_mesh_processing/polygon_soup_to_polygon_mesh.h>
#include <CGAL/Polygon_mesh_processing/repair.h>
#include <vector>
#include <array>
#include <cstdlib>
#include <cstring>

typedef CGAL::Exact_predicates_inexact_constructions_kernel K;
typedef K::Point_3 Point_3;
typedef CGAL::Surface_mesh<Point_3> Mesh;
namespace PMP = CGAL::Polygon_mesh_processing;

namespace {

// soup_to_mesh constructs a Surface_mesh from a triangle soup, orienting
// the soup first. Returns false and sets *err on failure.
bool soup_to_mesh(
    const float *vertices, int num_vertices,
    const int *faces, int num_faces,
    Mesh &out, char **err)
{
    std::vector<Point_3> pts;
    pts.reserve(num_vertices);
    for (int i = 0; i < num_vertices; i++) {
        pts.emplace_back(vertices[i*3], vertices[i*3+1], vertices[i*3+2]);
    }
    std::vector<std::array<std::size_t, 3>> tris;
    tris.reserve(num_faces);
    for (int i = 0; i < num_faces; i++) {
        tris.push_back({(std::size_t)faces[i*3],
                        (std::size_t)faces[i*3+1],
                        (std::size_t)faces[i*3+2]});
    }
    if (!PMP::orient_polygon_soup(pts, tris)) {
        *err = strdup("input mesh is non-orientable");
        return false;
    }
    PMP::polygon_soup_to_polygon_mesh(pts, tris, out);
    return true;
}

// mesh_to_cresult fills r with vertices/faces from mesh. Surface_mesh
// indices may have gaps after edits; remap to contiguous output.
bool mesh_to_cresult(const Mesh &mesh, struct CResult &r) {
    if (mesh.number_of_faces() == 0) {
        r.error = strdup("boolean produced empty mesh");
        return false;
    }
    std::vector<int> vmap(mesh.num_vertices() +
                          mesh.number_of_removed_vertices(), -1);
    r.num_vertices = (int)mesh.number_of_vertices();
    r.num_faces = (int)mesh.number_of_faces();
    r.vertices = (float*)malloc(r.num_vertices * 3 * sizeof(float));
    r.faces = (int*)malloc(r.num_faces * 3 * sizeof(int));
    if (!r.vertices || !r.faces) {
        free(r.vertices); free(r.faces);
        r.vertices = NULL; r.faces = NULL;
        r.num_vertices = 0; r.num_faces = 0;
        r.error = strdup("out of memory");
        return false;
    }

    int vi = 0;
    for (auto v : mesh.vertices()) {
        auto p = mesh.point(v);
        r.vertices[vi*3]   = (float)p.x();
        r.vertices[vi*3+1] = (float)p.y();
        r.vertices[vi*3+2] = (float)p.z();
        vmap[(std::size_t)v] = vi;
        vi++;
    }
    int fi = 0;
    for (auto f : mesh.faces()) {
        auto h = mesh.halfedge(f);
        auto h1 = mesh.next(h);
        auto h2 = mesh.next(h1);
        r.faces[fi*3]   = vmap[(std::size_t)mesh.target(h)];
        r.faces[fi*3+1] = vmap[(std::size_t)mesh.target(h1)];
        r.faces[fi*3+2] = vmap[(std::size_t)mesh.target(h2)];
        fi++;
    }
    return true;
}

enum BooleanOp { OP_UNION, OP_DIFFERENCE };

struct CResult run_boolean(
    const float *a_vertices, int a_num_vertices,
    const int *a_faces, int a_num_faces,
    const float *b_vertices, int b_num_vertices,
    const int *b_faces, int b_num_faces,
    BooleanOp op)
{
    struct CResult r = {};
    try {
        Mesh A, B, out;
        if (!soup_to_mesh(a_vertices, a_num_vertices, a_faces, a_num_faces, A, &r.error)) return r;
        if (!soup_to_mesh(b_vertices, b_num_vertices, b_faces, b_num_faces, B, &r.error)) return r;

        bool ok = false;
        switch (op) {
        case OP_UNION:
            ok = PMP::corefine_and_compute_union(A, B, out);
            break;
        case OP_DIFFERENCE:
            ok = PMP::corefine_and_compute_difference(A, B, out);
            break;
        }
        if (!ok) {
            r.error = strdup("CGAL boolean failed (likely self-intersection or non-closed input)");
            return r;
        }
        mesh_to_cresult(out, r);
    } catch (const std::exception &e) {
        free(r.vertices); free(r.faces);
        r.vertices = NULL; r.faces = NULL;
        r.num_vertices = 0; r.num_faces = 0;
        r.error = strdup(e.what());
    } catch (...) {
        r.error = strdup("unknown C++ exception in boolean");
    }
    return r;
}

} // namespace

extern "C" {

struct CResult cb_union(
    const float *a_vertices, int a_num_vertices,
    const int *a_faces, int a_num_faces,
    const float *b_vertices, int b_num_vertices,
    const int *b_faces, int b_num_faces)
{
    return run_boolean(a_vertices, a_num_vertices, a_faces, a_num_faces,
                       b_vertices, b_num_vertices, b_faces, b_num_faces,
                       OP_UNION);
}

struct CResult cb_difference(
    const float *a_vertices, int a_num_vertices,
    const int *a_faces, int a_num_faces,
    const float *b_vertices, int b_num_vertices,
    const int *b_faces, int b_num_faces)
{
    return run_boolean(a_vertices, a_num_vertices, a_faces, a_num_faces,
                       b_vertices, b_num_vertices, b_faces, b_num_faces,
                       OP_DIFFERENCE);
}

void cb_free(struct CResult r) {
    free(r.vertices);
    free(r.faces);
    free(r.error);
}

}
