# orzel_all_free

Ground-truth palette-search table for the **all-free orzel** fixture: all four
color slots open, searched over the curated 20-filament inventory.

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/orzel_przedni/Orzel_przedni.obj` |
| settings | `calibration/fixtures/orzel_all_free.json` |
| swept via | `calibration/sweep.sh` @ 6c84124 (2026-07-24, clip-triangle renders) |
| size | 50 mm, snapmaker_u1, layer 0.08 mm |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none — 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 58509 |
| sec/candidate | 0.292 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | free slots | rank_key |
| --- | --- | --- |
| 1 | Dark Grey \| Brown \| Tan \| Red (`#485259 #55331A #A79E82 #E72F1D`) | 4.166 |
| 2 | Brown \| Dark Olive Drab \| Tan \| Red (`#55331A #575B54 #A79E82 #E72F1D`) | 4.205 |
| 3 | Black \| Tan \| Red \| Yellow (`#080A0D #A79E82 #E72F1D #FFE800`) | 4.290 |
| 4 | Dark Grey \| Brown \| Lime Green \| Pink (`#485259 #55331A #D5D701 #F1A1AF`) | 4.314 |
| 5 | Black \| Tan \| Lime Green \| Red (`#080A0D #A79E82 #D5D701 #E72F1D`) | 4.394 |

## Production-scorer regret (tuned defaults wash=0.9 μ=0.30 ν=0)

```
REGRET: production fast scorer picked #080A0D(Black) #55331A(Brown) #8C9099(Grey) #A79E82(Tan)
        its rank 163/4845 (rankKey 6.286); winner rankKey 4.166; delta 2.120
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
- 2026-07-24: re-swept with real clipped cell-triangle renders (6c84124)
  replacing splat quads; winners and top ranks essentially unchanged vs the
  splat-era table, confirming splat artifacts blurred out of the metric.
