#ifndef CGALCLIP_CLIP_H
#define CGALCLIP_CLIP_H

#ifdef __cplusplus
extern "C" {
#endif

/* CResult mirrors AWResult in alphawrap: caller-owned C buffers with
 * vertices flat as 3 floats each, faces flat as 3 ints each, and
 * an error string (NULL on success). Free with cc_free. */
struct CResult {
    float *vertices;     /* 3 floats per vertex */
    int num_vertices;
    int *faces;          /* 3 ints per face */
    int num_faces;
    char *error;         /* NULL on success */
};

/* Clip the input mesh against an axis-aligned half-space and return the
 * remaining (closed, watertight) half. The plane is described by
 *   normal · p == d
 * and the kept half is the one where normal · p <= d (the "negative"
 * side). To get the other half, flip both `normal` and `d`. */
struct CResult cc_clip(
    const float *vertices, int num_vertices,
    const int *faces, int num_faces,
    double nx, double ny, double nz, double d);

void cc_free(struct CResult result);

#ifdef __cplusplus
}
#endif
#endif
