#include "rectclip.h"
#include "clipper2/clipper.rectclip.h"
// Compile Clipper2's RectClip implementation as part of this
// translation unit. Cgo only picks up *.cpp files in the same
// directory as the *.go binding, so vendoring the Clipper2 source
// in a subdirectory + including it here is the cleanest way to
// keep the vendored library tree intact while still getting it
// linked.
#include "clipper2/clipper.rectclip.cpp"

#include <cstdlib>
#include <cstring>
#include <vector>

using namespace Clipper2Lib;

struct rc_state {
  Paths64 paths;
};

extern "C" rc_state *rc_new(void) { return new rc_state(); }

extern "C" void rc_free(rc_state *s) { delete s; }

extern "C" void rc_set_paths(rc_state *s,
                             int num_paths, const int32_t *path_sizes,
                             int total_points, const int64_t *points) {
  s->paths.clear();
  s->paths.reserve(num_paths);
  size_t offset = 0;
  for (int i = 0; i < num_paths; ++i) {
    int n = path_sizes[i];
    Path64 path;
    path.reserve(n);
    for (int j = 0; j < n; ++j) {
      int64_t x = points[(offset + j) * 2];
      int64_t y = points[(offset + j) * 2 + 1];
      path.push_back(Point64(x, y));
    }
    s->paths.push_back(std::move(path));
    offset += n;
  }
  (void)total_points;
}

extern "C" rc_result rc_clip(rc_state *s,
                             int64_t x0, int64_t y0,
                             int64_t x1, int64_t y1) {
  rc_result out;
  out.num_paths = 0;
  out.path_sizes = nullptr;
  out.total_points = 0;
  out.points = nullptr;

  Rect64 rect(x0, y0, x1, y1);
  RectClip64 clipper(rect);
  Paths64 result = clipper.Execute(s->paths);
  if (result.empty()) {
    return out;
  }

  int total_points = 0;
  for (const auto &p : result) total_points += static_cast<int>(p.size());

  out.num_paths = static_cast<int>(result.size());
  out.total_points = total_points;
  out.path_sizes = (int32_t *)std::malloc(sizeof(int32_t) * out.num_paths);
  out.points = (int64_t *)std::malloc(sizeof(int64_t) * total_points * 2);

  int point_offset = 0;
  for (int i = 0; i < out.num_paths; ++i) {
    out.path_sizes[i] = static_cast<int32_t>(result[i].size());
    for (size_t j = 0; j < result[i].size(); ++j) {
      out.points[(point_offset + j) * 2] = result[i][j].x;
      out.points[(point_offset + j) * 2 + 1] = result[i][j].y;
    }
    point_offset += static_cast<int>(result[i].size());
  }
  return out;
}

extern "C" void rc_free_result(rc_result r) {
  std::free(r.path_sizes);
  std::free(r.points);
}
