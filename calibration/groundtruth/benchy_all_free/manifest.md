# benchy_all_free

Ground-truth palette-search table for the **rainbow benchy** fixture — the
3DBenchy hull painted with a 30 mm pride-rainbow MaterialX. Six saturated
hues spanning the whole gamut with only 4 slots: intrinsically
unsatisfiable, so absolute rank_key values run far above the other
fixtures. Compare deltas, not levels. The fixture is kept deliberately —
no palette can be right here, which is exactly what makes it a useful
stress test of *how* the scorer fails.

| field | value |
| --- | --- |
| model | `/home/tnewsome/Documents/3d_print/objects/3DBenchy.stl` |
| settings | `calibration/fixtures/benchy_all_free.json` |
| swept via | `calibration/sweep.sh` @ a0ced92 (2026-07-24, 30 mm texture re-sweep) |
| size | 40 mm, snapmaker_u1, layer 0.20 mm |
| texture | `pride_rainbow.mtlx`, tile 0.75×extent = 30 mm, triplanar sharpness 4 |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none → 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 23256 |
| sec/candidate | 0.258 |

## Texture rescale (2026-07-24)

The fixture previously used an 8 mm tile (`baseMaterialXTileMM: 0.2`). At
that scale the rainbow bands were finer than the cell grid could resolve —
the boat was a fine multi-hue stipple rather than legible stripes, and the
table measured the palette's ability to average a hue soup rather than to
reproduce distinct bands. The tile was rescaled in the GUI to 0.75×extent
= **30 mm**, giving one clean stripe per hull region (purple roof, blue
cabin, green mid, yellow band, orange hull, red keel). The new target
renders were reviewed and approved before this re-sweep.

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | palette | rank_key | p99(σ2) |
| --- | --- | --- | --- |
| 1 | Azure Blue \| Green \| Red \| Yellow (`#0066D9 #06924D #E72F1D #FFE800`) | 15.050 | 50.256 |
| 2 | Green \| Purple \| Red \| Yellow (`#06924D #6C47B2 #E72F1D #FFE800`) | 15.393 | 63.353 |
| 3 | Black \| Purple \| Red \| Yellow (`#080A0D #6C47B2 #E72F1D #FFE800`) | 16.708 | 60.580 |
| 4 | Azure Blue \| Black \| Red \| Yellow (`#0066D9 #080A0D #E72F1D #FFE800`) | 16.918 | 56.971 |
| 5 | Dark Grey \| Purple \| Red \| Yellow (`#485259 #6C47B2 #E72F1D #FFE800`) | 17.132 | 61.548 |

**Red + Yellow appear in all ten top entries** — the warm keel/hull/band
triple is the non-negotiable half of the rainbow, and the two remaining
slots argue over how to cover blue/green/purple. Purple is no longer
mandatory: the winner has none, synthesising the purple roof from Azure
Blue + Red. It still shows up in five of the top ten, so it remains a
strong but optional violet source.

## Production-scorer regret (tuned defaults wash=0.9 μ=0.30 ν=0)

```
REGRET: production fast scorer picked #0066D9(Azure Blue) #06924D(Green) #F67405(Orange) #FFE800(Yellow)
        its rank 51/4845 (rankKey 21.512); winner rankKey 15.050; delta 6.463
```

The pick differs from the winner in exactly one slot: **Orange `#F67405`
instead of Red `#E72F1D`**. Orange is the safer, more "central" warm
chroma by every closed-form term the scorer has, but the model needs a
true red for the keel, and Orange cannot get there — the dither ends up
mixing Orange with Azure Blue to fake it. Rank 51/4845 is respectable in
absolute terms; the 6.463 rank_key gap is the largest in the suite and
remains the closed-form scorer's plateau on this fixture.

The weight descent was re-run from scratch against this new table (the
`calibration/tune_log.csv` eval cache was deleted, since every cached
`d_benchy` referred to the old 8 mm table). It re-converged on exactly the
shipped defaults — wash=0.9 μ=0.30 ν=0 spread=0.3 mixsat=30, total
10.4430 / max 6.4630 — so **the delta was not tuned away**; no production
code or weight change came out of this re-sweep.

## Before/after (8 mm → 30 mm texture)

| | old (8 mm tile) | new (30 mm tile) |
| --- | --- | --- |
| winner | Black \| Purple \| Red \| Yellow | Azure Blue \| Green \| Red \| Yellow |
| winner rank_key | 22.094 | 15.050 |
| prod pick | Green \| Brown \| Purple \| Olive Green | Azure Blue \| Green \| Orange \| Yellow |
| prod-pick rank | 414/4845 (rankKey 30.776) | 51/4845 (rankKey 21.512) |
| delta | 8.682 | 6.463 |
| Purple | in all top 10; the only violet source | absent from the winner, in 5 of top 10 |

Legible bands lowered the whole score column (a 4-colour palette can
actually cover broad stripes) and changed the failure mode: the old pick
lost an entire hue family (earth tones for the warm gradient, olive mud
across the lower hull), the new pick loses one hue *shade* (Orange for
Red). Both the scorer and the ground truth got sharper.

## Notes

- **Wash-ladder finding.** Under the old 8 mm table the WASH ladder had a
  unique optimum (0.2–0.75 all scored total 17.474 versus 12.662 at 0.9).
  Under the new table the ladder is **flat from 0.2 through 0.9** (all
  total 10.443), worsening only at 1.05+. The degenerate fine-band benchy
  was the fixture pinning the wash weight from below; the suite no longer
  constrains it there. wash=0.9 is retained because nothing in the suite
  prefers less — not because evidence pins it.
- The very first sweep of this fixture was degenerate in a different way:
  the fixture JSON's `sizeRelativeUnits` marker made `baseMaterialXTileMM:
  8` mean 8×extent (320 mm), painting the whole boat one flat stripe —
  constant ΔE at every blur σ. Fixed in 86c0c08; a flat, σ-invariant score
  column is the signature of that failure.
- `inputFile` and the .mtlx are absolute paths to the author's local
  library; not committed.
- 2026-07-24 (earlier): re-swept with real clipped cell-triangle renders
  (6c84124) replacing splat quads; winners and top ranks were essentially
  unchanged vs the splat-era table, confirming splat artifacts blurred out
  of the metric. This re-sweep inherits that render path.

## Tuned-pick comparison (2026-07-24, 30 mm texture)

Renders come from real clipped cell-triangle geometry and already apply
the print-sim neighbor-blend model: candidate visible colors come from
`voxel.EffectiveCellColors` with `simNeighborParams(LayerHeight)`, so no
separate print-sim pass is needed — these renders *are* the print sim.
Side-by-side montage (perspective view): `compare_tuned_vs_winner.png` —
panels: sampled target · winner · prod pick. Rebuilt by
`calibration/montage.sh`, which refuses to run on renders older than the
fixture JSON and reads its panel labels out of `sweep.log`, so a re-swept
table cannot be captioned with the previous run's palettes.

Visual read: the two candidates agree over most of the boat — green mid
band, yellow band and orange hull are near-identical, and both approximate
the purple roof the same way (a blue base flecked with their warm colour,
winner reading a cooler magenta-violet, prod pick a warmer pink-salmon
violet). The one decisive difference is the **keel**: the target's deep
solid red band is reproduced almost exactly by the winner's Red, while the
prod pick — having no red — renders it as a mottled orange/lavender
speckle, noticeably lighter and pinker, with visible blue flecks breaking
up what should be a flat red waterline. The gap is real and localized
rather than diffuse: on a casual look the two boats read as the same
model, but the keel is the first thing the eye lands on and it is
obviously wrong in the pick. Delta 6.463 is a single-region failure, not a
whole-model collapse like the old table's.
