# earth_all_free

Ground-truth palette-search table for the **earth globe** fixture — a
textured sphere with large saturated regions (ocean blue, landmass
green/tan, cloud white). Exercises the nominal (non-TD) scorer path:
`honorTD` is **false** here, so the production pick comes from the
un-hardened nominal hull scorer, not the TD-aware one.

| field | value |
| --- | --- |
| model | `tests/objects/earth.glb` (committed test object) |
| settings | `calibration/fixtures/earth_all_free.json` |
| swept via | `calibration/sweep.sh` @ 66ab61a (2026-07-23) |
| size | 40 mm, snapmaker_u1, layer 0.20 mm |
| dither | riemersma (bias 0.85), honorTD **false**, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none → 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 29021 |
| sec/candidate | 0.535 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | palette | rank_key |
| --- | --- | --- |
| 1 | Blue \| Polymaker Teal \| Olive Green \| Pink | 14.334 |
| 2 | Blue \| Polymaker Teal \| Olive Green \| Cold White | 14.437 |
| 3 | Blue \| Green \| Olive Green \| Cold White | 14.714 |
| 4 | Blue \| Polymaker Teal \| Tan \| Cold White | 15.006 |
| 5 | Blue \| Azure Blue \| Polymaker Teal \| Tan | 15.274 |

## Production-scorer regret

```
REGRET: production fast scorer picked #0066D9(Azure Blue) #080A0D(Black) #4CC0C7(Polymaker Teal) #FFE800(Yellow)
        its rank 394/4845 (rankKey 20.348); winner #003776(Blue) #4CC0C7(Polymaker Teal) #948902(Olive Green) #F1A1AF(Pink) rankKey 14.334; delta 6.014
```

Largest regret in the suite. Every top-10 ground-truth entry anchors on
opaque Blue (#003776); the scorer instead takes translucent Azure Blue
plus Black and leaky Yellow (TD 4.3) — hull-corner reach over deliverable
color. The greedy nominal path evaluated only 204 subsets.

## Notes

- Absolute rank_key values run higher than the orzel fixtures; the globe
  has large regions far from any single filament, so even the winner
  carries substantial ΔE. Compare deltas, not absolute levels, across
  fixtures.
