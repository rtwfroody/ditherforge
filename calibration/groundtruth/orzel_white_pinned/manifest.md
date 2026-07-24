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
| swept via | `calibration/sweep.sh` @ 66ab61a (2026-07-23) |
| size | 50 mm, snapmaker_u1, layer 0.08 mm |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | `#EBF7FF` White (slot 4) → 3 free slots, 19 eligible |
| candidates | 969 = C(19,3) |
| cells | 58509 |
| sec/candidate | 0.270 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | free slots | rank_key |
| --- | --- | --- |
| 1 | Brown \| Tan \| Orange (`#55331A #A79E82 #F67405`) | 5.211 |
| 2 | Brown \| Tan \| Red (`#55331A #A79E82 #E72F1D`) | 5.643 |
| 3 | Black \| Tan \| Orange (`#080A0D #A79E82 #F67405`) | 5.782 |
| 4 | Brown \| Lime Green \| Pink (`#55331A #D5D701 #F1A1AF`) | 5.906 |
| 5 | Brown \| Orange \| Yellow (`#55331A #F67405 #FFE800`) | 5.913 |

The winner matches rank 2 of the 28-color table exactly (Brown|Tan|Orange,
5.211) — the curated trim cost only the Beige-based rank-1 (5.048).

## Production-scorer regret (default μ=0.15 ν=0.08 wash=0.6)

```
REGRET: production fast scorer picked #485259(Dark Grey) #55331A(Brown) #A79E82(Tan)
        its rank 26/969 (rankKey 7.077); winner #55331A(Brown) #A79E82(Tan) #F67405(Orange) rankKey 5.211; delta 1.866
```

With Beige gone the scorer no longer reaches for Purple — its pick is a
sane earthy triple at rank 26/969. The 28-color Purple pathology remains
reproducible from `d098f98`. The residual miss pattern here: it spends the
third slot on a second dark (Dark Grey) instead of the chroma carrier
(Orange/Red) every top entry uses.

## Notes

- `inputFile` is an absolute path to the author's local model library; the
  table is reproducible only where that model exists. The model is
  intentionally not committed.
- Renders: `target_front.png` (sampled target), `prodpick_*.png`, and
  `top1..top5` candidate renders.
