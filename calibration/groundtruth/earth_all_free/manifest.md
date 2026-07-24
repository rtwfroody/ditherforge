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
| swept via | `calibration/sweep.sh` @ 6c84124 (2026-07-24, clip-triangle renders) |
| size | 40 mm, snapmaker_u1, layer 0.20 mm |
| dither | riemersma (bias 0.85), honorTD **false**, colorAwareCells true |
| inventory | curated 20-filament subset (`calibration/fixtures/panchroma_curated20.txt`) |
| locked | none → 4 free slots |
| candidates | 4845 = C(20,4) |
| cells | 29021 |
| sec/candidate | 0.544 |

## Winner + top 5 (rank_key = mean ΔE at σ∈{2,8})

| rank | palette | rank_key |
| --- | --- | --- |
| 1 | Blue \| Polymaker Teal \| Olive Green \| Pink | 13.410 |
| 2 | Blue \| Polymaker Teal \| Olive Green \| Cold White | 13.464 |
| 3 | Blue \| Green \| Olive Green \| Cold White | 13.877 |
| 4 | Blue \| Polymaker Teal \| Olive Green \| Magenta | 14.450 |
| 5 | Blue \| Polymaker Teal \| Tan \| Cold White | 14.504 |

## Production-scorer regret

```
REGRET: production fast scorer picked #003776(Blue) #4CC0C7(Polymaker Teal) #948902(Olive Green) #D9DFE5(Cold White)
        its rank 2/4845 (rankKey 13.464); winner rankKey 13.410; delta 0.055
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
- 2026-07-24: re-swept with real clipped cell-triangle renders (6c84124)
  replacing splat quads; winners and top ranks essentially unchanged vs the
  splat-era table, confirming splat artifacts blurred out of the metric.
