# orzel_white_pinned

Ground-truth palette-search table for the **White-pinned orzel** fixture — the
case that exposed the scorer overfit (μ/ν were tuned on all-free orzel and
misbehaved once White was locked).

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/orzel_przedni/Orzel_przedni.obj` |
| settings | `calibration/fixtures/orzel_white_pinned.json` |
| harness commit (table) | `9e75607` (sweep run 2026-07-23) |
| harness commit (regret baseline) | `f156cd9` |
| date | 2026-07-23 |
| size | 50 mm, snapmaker_u1, layer 0.08 mm |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | Panchroma Basic (28 colors) |
| locked | `#EBF7FF` White (slot 4) → 3 free slots, 27 eligible |
| candidates | 2925 = C(27,3) |
| cells | 58509 |
| sec/candidate | 0.326 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | free slots | rank_key |
| --- | --- | --- |
| 1 | Brown \| Beige \| Pink (`#55331A #C2AB72 #F1A1AF`) | 5.048 |
| 2 | Brown \| Tan \| Orange (`#55331A #A79E82 #F67405`) | 5.211 |
| 3 | Black \| Brown \| Cream (`#080A0D #55331A #EED1A8`) | 5.359 |
| 4 | Dark Grey \| Brown \| Cream (`#485259 #55331A #EED1A8`) | 5.413 |
| 5 | Brown \| Beige \| Magenta (`#55331A #C2AB72 #F24574`) | 5.541 |

## Production-scorer regret (default μ=0.15 ν=0.08 wash=0.6)

```
REGRET: production fast scorer picked #080A0D(Black) #6C47B2(Purple) #C2AB72(Beige)
        its rank 359/2925 (rankKey 9.980); winner #55331A(Brown) #C2AB72(Beige) #F1A1AF(Pink) rankKey 5.048; delta 4.933
```

The scorer picks a Black/Purple/Beige palette that lands at rank **359/2925**
with a rank-key regret of **4.933** — it forgoes the Brown workhorse that every
top table entry keeps, and adds Purple (a hull-spanner) instead. This is the
headline miss the calibration suite exists to drive down.

## Notes

- `inputFile` is an absolute path to the author's local model library; the
  table is reproducible only where that model exists. The model is intentionally
  not committed.
- Renders: `target_front.png` (sampled target) and `prodpick_{front,persp}.png`
  are included. Top-N candidate renders were not saved by the original sweep.
