# benchy_all_free

Ground-truth palette-search table for the **rainbow benchy** fixture — the
3DBenchy hull painted with an 8 mm pride-rainbow MaterialX. Saturated hues
spanning the whole gamut with only 4 slots: intrinsically hard, so
absolute rank_key values run far above the other fixtures. Compare
deltas, not levels.

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/3DBenchy.stl` |
| settings | `calibration/fixtures/benchy_all_free.json` |
| swept via | `calibration/sweep.sh` @ 6c84124 (2026-07-24, clip-triangle renders) |
| size | 40 mm, snapmaker_u1, layer 0.20 mm |
| texture | `pride_rainbow.mtlx`, tile 0.2×extent = 8 mm, triplanar sharpness 4 |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none → 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 23205 |
| sec/candidate | 0.291 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | palette | rank_key |
| --- | --- | --- |
| 1 | Black \| Purple \| Red \| Yellow | 22.094 |
| 2 | Black \| Purple \| Lime Green \| Red | 22.270 |
| 3 | Blue \| Purple \| Red \| Yellow | 22.408 |
| 4 | Blue \| Purple \| Lime Green \| Red | 23.001 |
| 5 | Brown \| Purple \| Red \| Yellow | 23.149 |

Purple appears in ALL top-10 entries — it is the only violet source for
the rainbow's purple stripe. This fixture is the mirror image of the
orzel Purple bug: here the scorer must NOT avoid Purple.

## Production-scorer regret

```
REGRET: production fast scorer picked #06924D(Green) #55331A(Brown) #6C47B2(Purple) #948902(Olive Green)
        its rank 414/4845 (rankKey 30.776); winner rankKey 22.094; delta 8.682
```

Worst regret in the suite, and the closed-form scorer's plateau: no
weight vector on the current five terms gets below ~8.7 here without
regressing the orzels (coordinate descent converged twice, splat and
clip-triangle tables alike). The tuned pick does carry Purple (the
pre-tuning pick's worst omission) but substitutes earth-tone anchors
(Brown, Olive Green) for the Black/Red/Yellow extremes, collapsing the
warm lower-hull rainbow bands into olive/mustard. The scorer lacks any
hue-family-coverage notion — it measures distances, not whether a warm-red
source exists at all.

## Notes

- The first sweep of this fixture was degenerate: the fixture JSON's
  `sizeRelativeUnits` marker made `baseMaterialXTileMM: 8` mean 8×extent
  (320 mm), painting the whole boat one flat stripe — constant ΔE at every
  blur σ. Fixed to 0.2 in 86c0c08; a flat, σ-invariant score column is the
  signature of this failure.
- `inputFile` and the .mtlx are absolute paths to the author's local
  library; not committed.
- 2026-07-24: re-swept with real clipped cell-triangle renders (6c84124)
  replacing splat quads; winners and top ranks essentially unchanged vs the
  splat-era table, confirming splat artifacts blurred out of the metric.

## Tuned-pick comparison (2026-07-24)

The production fast scorer's pick for this fixture is **Green `#06924D` |
Brown `#55331A` | Purple `#6C47B2` | Olive Green `#948902`** — rank
**414/4845**, rankKey **30.776**, delta **8.682** vs the winner (Black |
Purple | Red | Yellow, rankKey 22.094).

Rendered from the real clipped cell-triangle geometry (no splat-quad gap
artifacts) into `prodpick_*.png`. The harness render path already applies
the print-sim neighbor-blend model: candidate visible colors come from
`voxel.EffectiveCellColors` with `simNeighborParams(LayerHeight)`, so no
separate print-sim pass is needed — these renders *are* the print sim.
Side-by-side montage (perspective view): `compare_tuned_vs_winner.png` —
panels: sampled target · winner · prod pick.

Visual read (clip-triangle renders): the **upper hull** is a near-tie — both
pick and winner carry real Purple, so the large purple cabin/body matches
the target. The winner's Purple+Red+Black speckle reads slightly magenta
and darker; the prod pick's Purple+Brown is arguably a touch closer to the
target's clean purple up top. The pick's decisive failure is the **lower
hull warm gradient**: the target's blue→green→yellow→orange→red waterline
collapses to olive/mustard+brown mud in the prod pick, because the palette
has no warm-red or true-yellow source (Olive Green `#948902` is its only
warm-ish note). The winner reproduces the gradient faithfully — a clear
yellow band above a red/orange keel. The delta 8.682 is concentrated and
obvious, not subtle: a wrong hue-family bottom third on the model's most
colorful feature, visible at any distance and plausibly objectionable on a
real print. The prior splat-based assessment holds on the clean renders.
