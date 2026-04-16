#ifndef ALPHAWRAP_WRAP_H
#define ALPHAWRAP_WRAP_H

#ifdef __cplusplus
extern "C" {
#endif

struct AWResult {
    float *vertices;     /* 3 floats per vertex */
    int num_vertices;
    int *faces;          /* 3 ints per face */
    int num_faces;
    char *error;         /* NULL on success, strdup'd message on failure */
};

struct AWResult aw_alpha_wrap(
    const float *vertices, int num_vertices,
    const int *faces, int num_faces,
    double alpha, double offset);

void aw_free(struct AWResult result);

#ifdef __cplusplus
}
#endif
#endif
