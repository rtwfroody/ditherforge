# TODO

## Longest-edge bisection

Replace the current midpoint subdivision (which splits all 3 edges of every
too-long face) with longest-edge bisection: only split the longest edge of each
triangle, adding a midpoint there and updating the two neighbor faces that share
that edge. This avoids repeatedly halving already-short edges, producing fewer
triangles for the same max-edge-length target and making better use of the 1M
vertex budget on large models.
