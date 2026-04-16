#include "wrap.h"
#include <CGAL/Exact_predicates_inexact_constructions_kernel.h>
#include <CGAL/Surface_mesh.h>
#include <CGAL/alpha_wrap_3.h>
#include <vector>
#include <array>
#include <cstdlib>
#include <cstring>

typedef CGAL::Exact_predicates_inexact_constructions_kernel K;
typedef K::Point_3 Point_3;
typedef CGAL::Surface_mesh<Point_3> Mesh;

extern "C" {

struct AWResult aw_alpha_wrap(
    const float *vertices, int num_vertices,
    const int *faces, int num_faces,
    double alpha, double offset)
{
    struct AWResult r = {};
    try {
        std::vector<Point_3> pts;
        pts.reserve(num_vertices);
        for (int i = 0; i < num_vertices; i++)
            pts.emplace_back(vertices[i*3], vertices[i*3+1], vertices[i*3+2]);

        std::vector<std::array<std::size_t, 3>> tris;
        tris.reserve(num_faces);
        for (int i = 0; i < num_faces; i++)
            tris.push_back({(std::size_t)faces[i*3],
                            (std::size_t)faces[i*3+1],
                            (std::size_t)faces[i*3+2]});

        Mesh output;
        CGAL::alpha_wrap_3(pts, tris, alpha, offset, output);

        r.num_vertices = (int)output.number_of_vertices();
        r.num_faces = (int)output.number_of_faces();
        r.vertices = (float*)malloc(r.num_vertices * 3 * sizeof(float));
        r.faces = (int*)malloc(r.num_faces * 3 * sizeof(int));
        if (!r.vertices || !r.faces) {
            free(r.vertices);
            free(r.faces);
            r.vertices = NULL;
            r.faces = NULL;
            r.num_vertices = 0;
            r.num_faces = 0;
            r.error = strdup("out of memory");
            return r;
        }

        // Build vertex-descriptor to sequential-index map.
        // Surface_mesh indices may have gaps if vertices were removed.
        std::vector<int> vmap(output.num_vertices() +
                              output.number_of_removed_vertices(), -1);
        int vi = 0;
        for (auto v : output.vertices()) {
            auto p = output.point(v);
            r.vertices[vi*3]   = (float)p.x();
            r.vertices[vi*3+1] = (float)p.y();
            r.vertices[vi*3+2] = (float)p.z();
            vmap[(std::size_t)v] = vi;
            vi++;
        }

        int fi = 0;
        for (auto f : output.faces()) {
            auto h = output.halfedge(f);
            auto h1 = output.next(h);
            auto h2 = output.next(h1);
            r.faces[fi*3]   = vmap[(std::size_t)output.target(h)];
            r.faces[fi*3+1] = vmap[(std::size_t)output.target(h1)];
            r.faces[fi*3+2] = vmap[(std::size_t)output.target(h2)];
            fi++;
        }
    } catch (const std::exception &e) {
        r.error = strdup(e.what());
    } catch (...) {
        r.error = strdup("unknown C++ exception in alpha_wrap");
    }
    return r;
}

void aw_free(struct AWResult r) {
    free(r.vertices);
    free(r.faces);
    free(r.error);
}

}
