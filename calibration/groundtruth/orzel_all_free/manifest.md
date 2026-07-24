# orzel_all_free

Ground-truth palette-search table for the **all-free orzel** fixture: all four
color slots open, searched over the curated 20-filament inventory.

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/orzel_przedni/Orzel_przedni.obj` |
| settings | `calibration/fixtures/orzel_all_free.json` |
| harness commit (table) | `93f6f9f` (sweep run 2026-07-23) |
| date | 2026-07-23 |
| size | 50 mm, snapmaker_u1, layer 0.08 mm |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none — 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 58509 |
| sec/candidate | 0.274 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | free slots | rank_key |
| --- | --- | --- |
| 1 | Dark Grey \| Brown \| Tan \| Red (`#485259 #55331A #A79E82 #E72F1D`) | 5.158 |
| 2 | Brown \| Dark Olive Drab \| Tan \| Red (`#55331A #575B54 #A79E82 #E72F1D`) | 5.162 |
| 3 | Brown \| Dark Olive Drab \| Tan \| Orange (`#55331A #575B54 #A79E82 #F67405`) | 5.191 |
| 4 | Dark Grey \| Brown \| Tan \| Orange (`#485259 #55331A #A79E82 #F67405`) | 5.204 |
| 5 | Brown \| Tan \| White \| Orange (`#55331A #A79E82 #EBF7FF #F67405`) | 5.211 |

## Production-scorer regret (default μ=0.15 ν=0.08 wash=0.6)

```
REGRET: production fast scorer picked #080A0D(Black) #55331A(Brown) #575B54(Dark Olive Drab) #A79E82(Tan)
        its rank 199/4845 (rankKey 7.262); winner #485259(Dark Grey) #55331A(Brown) #A79E82(Tan) #E72F1D(Red) rankKey 5.158; delta 2.105
```

The production scorer lands at rank **199/4845** with a rank-key regret of
**2.105**. It keeps the Brown+Tan workhorse pair that every top entry shares,
but spends its two remaining slots on Black + Dark Olive Drab (two dark
near-neutrals) instead of the Dark-Grey/Red split the top table prefers — it
under-values the warm Red accent the winner uses to carry the eagle's plumage.
No hull-spanner this time: with the curated inventory (Beige dropped, Purple
retained) the scorer does not reach for Purple here, so the miss is a
neutral-over-accent bias rather than the opaque-hull-spanner failure seen on
White-pinned orzel.

## Notes

- `inputFile` is an absolute path to the author's local model library; the
  table is reproducible only where that model exists. The model is intentionally
  not committed.
- Renders: `target_{front,side,top,persp}.png` (sampled target),
  `top{1..5}_*.png` (ground-truth top-5), and `prodpick_*.png` (production
  scorer pick) are included.
