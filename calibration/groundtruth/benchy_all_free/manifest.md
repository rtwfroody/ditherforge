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
| swept via | `calibration/sweep.sh` @ 86c0c08 (2026-07-23) |
| size | 40 mm, snapmaker_u1, layer 0.20 mm |
| texture | `pride_rainbow.mtlx`, tile 0.2×extent = 8 mm, triplanar sharpness 4 |
| dither | dlc-d30-p7, honorTD true, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none → 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 23205 |
| sec/candidate | 0.261 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | palette | rank_key |
| --- | --- | --- |
| 1 | Black \| Purple \| Red \| Yellow | 22.441 |
| 2 | Blue \| Purple \| Red \| Yellow | 22.562 |
| 3 | Black \| Purple \| Lime Green \| Red | 22.688 |
| 4 | Blue \| Purple \| Lime Green \| Red | 23.177 |
| 5 | Dark Grey \| Purple \| Red \| Yellow | 23.376 |

Purple appears in ALL top-10 entries — it is the only violet source for
the rainbow's purple stripe. This fixture is the mirror image of the
orzel Purple bug: here the scorer must NOT avoid Purple.

## Production-scorer regret

```
REGRET: production fast scorer picked #0066D9(Azure Blue) #06924D(Green) #F24574(Magenta) #F67405(Orange)
        its rank 940/4845 (rankKey 35.727); winner #080A0D(Black) #6C47B2(Purple) #E72F1D(Red) #FFE800(Yellow) rankKey 22.441; delta 13.286
```

Worst regret in the suite. The scorer picks four mid-saturation
mid-hues; ground truth wants gamut extremes (Black, Purple, Red, Yellow)
that dither can mix toward everything between.

## Notes

- The first sweep of this fixture was degenerate: the fixture JSON's
  `sizeRelativeUnits` marker made `baseMaterialXTileMM: 8` mean 8×extent
  (320 mm), painting the whole boat one flat stripe — constant ΔE at every
  blur σ. Fixed to 0.2 in 86c0c08; a flat, σ-invariant score column is the
  signature of this failure.
- `inputFile` and the .mtlx are absolute paths to the author's local
  library; not committed.
