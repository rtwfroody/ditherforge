// Clip a triangle mesh against a half-space using CGAL's
// Polygon_mesh_processing::clip(). The kept half is the side where
// normal·p <= d. The clipped output is a closed, oriented triangle
// mesh (the cap is added by CGAL during clipping).

#include "clip.h"
#include <CGAL/Exact_predicates_inexact_constructions_kernel.h>
#include <CGAL/Surface_mesh.h>
#include <CGAL/Polygon_mesh_processing/clip.h>
#include <CGAL/Polygon_mesh_processing/orient_polygon_soup.h>
#include <CGAL/Polygon_mesh_processing/polygon_soup_to_polygon_mesh.h>
#include <CGAL/Polygon_mesh_processing/repair.h>
#include <vector>
#include <array>
#include <cstdlib>
#include <cstring>

typedef CGAL::Exact_predicates_inexact_constructions_kernel K;
typedef K::Point_3 Point_3;
typedef K::Plane_3 Plane_3;
typedef CGAL::Surface_mesh<Point_3> Mesh;
namespace PMP = CGAL::Polygon_mesh_processing;

extern "C" {

struct CResult cc_clip(
    const float *vertices, int num_vertices,
    const int *faces, int num_faces,
    double nx, double ny, double nz, double d)
{
    struct CResult r = {};
    try {
        // Build a polygon soup, then orient it into a mesh. Same path
        // as alpha-wrap's input prep — handles non-manifold inputs
        // by orienting the soup before mesh construction.
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

        Mesh mesh;
        if (!PMP::orient_polygon_soup(pts, tris)) {
            r.error = strdup("input mesh is non-orientable");
            return r;
        }
        PMP::polygon_soup_to_polygon_mesh(pts, tris, mesh);

        // Plane orientation: clip() keeps the negative side of the
        // CGAL plane, where the plane is normal·p + d_cgal = 0.
        // Our convention is normal·p <= d, so d_cgal = -d.
        Plane_3 plane(nx, ny, nz, -d);

        // clip_volume=true asks PMP::clip to seal the resulting cut
        // surface so the output is a closed solid (the cap is added
        // automatically). The throw_on_self_intersection flag is on
        // so we surface bad inputs rather than producing garbage.
        PMP::clip(mesh, plane,
                  CGAL::parameters::clip_volume(true)
                                   .throw_on_self_intersection(true));

        if (mesh.number_of_faces() == 0) {
            r.error = strdup("clip produced empty mesh (plane misses input or input degenerate)");
            return r;
        }

        // Surface_mesh indices may have gaps after edits; remap to
        // contiguous output indices.
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
            return r;
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
    } catch (const std::exception &e) {
        free(r.vertices); free(r.faces);
        r.vertices = NULL; r.faces = NULL;
        r.num_vertices = 0; r.num_faces = 0;
        r.error = strdup(e.what());
    } catch (...) {
        r.error = strdup("unknown C++ exception in clip");
    }
    return r;
}

void cc_free(struct CResult r) {
    free(r.vertices);
    free(r.faces);
    free(r.error);
}

}
