#ifndef CGALBOOL_CGALBOOL_H
#define CGALBOOL_CGALBOOL_H

#ifdef __cplusplus
extern "C" {
#endif

/* CResult mirrors cgalclip's CResult: caller-owned C buffers with
 * vertices flat as 3 floats each, faces flat as 3 ints each, and an
 * error string (NULL on success). Free with cb_free. */
struct CResult {
    float *vertices;     /* 3 floats per vertex */
    int num_vertices;
    int *faces;          /* 3 ints per face */
    int num_faces;
    char *error;         /* NULL on success */
};

/* Compute (a ∪ b). Both inputs are closed triangle meshes. Returns the
 * union as a closed triangle mesh. */
struct CResult cb_union(
    const float *a_vertices, int a_num_vertices,
    const int *a_faces, int a_num_faces,
    const float *b_vertices, int b_num_vertices,
    const int *b_faces, int b_num_faces);

/* Compute (a \ b). Returns a minus b as a closed triangle mesh. */
struct CResult cb_difference(
    const float *a_vertices, int a_num_vertices,
    const int *a_faces, int a_num_faces,
    const float *b_vertices, int b_num_vertices,
    const int *b_faces, int b_num_faces);

void cb_free(struct CResult result);

#ifdef __cplusplus
}
#endif
#endif
