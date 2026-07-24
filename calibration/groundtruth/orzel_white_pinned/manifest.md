# orzel_white_pinned

Ground-truth palette-search table for the **White-pinned orzel** fixture —
the case that exposed the scorer overfit (μ/ν were tuned on all-free orzel
and misbehaved once White was locked).

**This table uses the curated 20-filament inventory.** It replaces the
original 28-color Panchroma Basic table (git `d098f98`), rerun so the
whole suite shares one inventory. Under 28 colors the winner was
Brown|Beige|Pink (rankKey 5.048) and the production scorer picked
Black|**Purple**|Beige — rank 359/2925, regret 4.933, the headline
pathology. Beige is not in the curated set; see the regret section for
how the pick changed.

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/orzel_przedni/Orzel_przedni.obj` |
| settings | `calibration/fixtures/orzel_white_pinned.json` |
| swept via | `calibration/sweep.sh` @ 6c84124 (2026-07-24, clip-triangle renders) |
| size | 50 mm, snapmaker_u1, layer 0.08 mm |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | `#EBF7FF` White (slot 4) → 3 free slots, 19 eligible |
| candidates | 969 = C(19,3) |
| cells | 58509 |
| sec/candidate | 0.297 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | free slots | rank_key |
| --- | --- | --- |
| 1 | Brown \| Tan \| Orange (`#55331A #A79E82 #F67405`) | 4.418 |
| 2 | Brown \| Tan \| Red (`#55331A #A79E82 #E72F1D`) | 4.683 |
| 3 | Black \| Tan \| Orange (`#080A0D #A79E82 #F67405`) | 4.729 |
| 4 | Brown \| Lime Green \| Pink (`#55331A #D5D701 #F1A1AF`) | 4.822 |
| 5 | Brown \| Lime Green \| Orange (`#55331A #D5D701 #F67405`) | 4.880 |

The winner matches rank 2 of the 28-color table exactly (Brown|Tan|Orange,
5.211) — the curated trim cost only the Beige-based rank-1 (5.048).

## Production-scorer regret (tuned defaults wash=0.9 μ=0.30 ν=0)

```
REGRET: production fast scorer picked #080A0D(Black) #55331A(Brown) #A79E82(Tan)
        its rank 24/969 (rankKey 6.223); winner rankKey 4.418; delta 1.805
```

With Beige gone the scorer no longer reaches for Purple — its pick is a
sane earthy triple at rank 24/969. The 28-color Purple pathology remains
reproducible from `d098f98` (and the tuned weights fix it there too:
Black|Brown|Tan, rank 83/2925, delta 1.919). The residual miss pattern:
it spends the third slot on Black instead of the chroma carrier
(Orange/Red) every top entry uses.

## Notes

- `inputFile` is an absolute path to the author's local model library; the
  table is reproducible only where that model exists. The model is
  intentionally not committed.
- Renders: `target_front.png` (sampled target), `prodpick_*.png`, and
  `top1..top5` candidate renders.
- 2026-07-24: re-swept with real clipped cell-triangle renders (6c84124)
  replacing splat quads; winners and top ranks essentially unchanged vs the
  splat-era table, confirming splat artifacts blurred out of the metric.
