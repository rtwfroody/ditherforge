#ifndef RECTCLIP_H
#define RECTCLIP_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* rc_state is an opaque handle that owns a cached set of polygon
 * paths in C++ memory. Per layer-face we load the exposed polygon
 * once and then run thousands of per-tile rect-clip operations
 * against it, so amortising the path-loading cost across all the
 * clips on a layer is worth the small interface complication. */
typedef struct rc_state rc_state;

/* Create a new state. The cached path list is initially empty;
 * call rc_set_paths before rc_clip. */
rc_state *rc_new(void);

/* Free a state. */
void rc_free(rc_state *s);

/* Replace the cached paths with the flat-encoded input. `points`
 * is `total_points * 2` int64s laid out as (x,y) pairs.
 * `path_sizes[i]` is the number of points in path i (so the
 * vertices of path i start at offset `sum(path_sizes[0..i-1])`
 * in `points`). Each path is closed implicitly: the last edge
 * goes from points[last] back to points[first]; don't repeat
 * the first point. */
void rc_set_paths(rc_state *s,
                  int num_paths, const int32_t *path_sizes,
                  int total_points, const int64_t *points);

/* rc_result is the output of one rc_clip call: a list of paths
 * laid out in the same flat encoding as rc_set_paths' input.
 * Caller must free with rc_free_result. */
typedef struct {
  int num_paths;
  int32_t *path_sizes;
  int total_points;
  int64_t *points;
} rc_result;

/* Clip the cached paths against the axis-aligned rectangle
 * [x0, x1] x [y0, y1]. Returns a list of clipped paths. */
rc_result rc_clip(rc_state *s,
                  int64_t x0, int64_t y0, int64_t x1, int64_t y1);

void rc_free_result(rc_result r);

#ifdef __cplusplus
}
#endif

#endif
