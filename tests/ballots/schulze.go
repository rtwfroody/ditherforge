package ballots

import "sort"

// BallotTiers is one ballot's epsilon-grouped ranking, best tier first.
// Modes within the same inner slice are tied (their scores fell within
// the epsilon window and express no mutual preference).
type BallotTiers struct {
	Voter  string
	Group  string  // normalized group ("2d"/"3d"); unknown groups map to "2d"
	Weight float64 // per-ballot weight actually applied
	Tiers  [][]string
}

// RankResult is the full output of a weighted Schulze election, carrying
// enough detail for both a terse ranking table and a verbose dump.
type RankResult struct {
	// Candidates is the union of every ballot's scored modes, sorted.
	Candidates []string
	// Order is the final ranking, best tier first; modes tied on Schulze
	// win count share an inner slice.
	Order [][]string
	// Pairwise is the weighted pairwise-defeat matrix d[a][b] = total
	// weight of ballots ranking a strictly better than b.
	Pairwise map[string]map[string]float64
	// Strength is the Schulze widest-path strength p[a][b].
	Strength map[string]map[string]float64
	// Wins maps each mode to its Schulze win count (number of modes it
	// beats via p[a][b] > p[b][a]).
	Wins map[string]int
	// MeanRank maps each mode to its mean 1-based tier position across
	// the ballots that scored it (a human sanity column).
	MeanRank map[string]float64
	// Ballots holds each ballot's tiering, in input order.
	Ballots []BallotTiers

	// Group bookkeeping for the summary line.
	N2d, N3d int     // ballot counts per normalized group
	W2d, W3d float64 // per-ballot weight applied within each group
	NUnknown int     // ballots whose group was neither "2d" nor "3d"

	EpsRel, EpsAbs float64
	GroupWeight2d  float64
	GroupWeight3d  float64
}

// tierScores groups (mode -> score) into equal-rank tiers, best (lowest
// score) first. Scores are sorted ascending; a score joins the current
// tier while score-tierFirst <= max(epsRel*tierFirst, epsAbs), else it
// opens a new tier. Ties in score are broken by mode name so the tiering
// is deterministic.
func tierScores(scores map[string]float64, epsRel, epsAbs float64) [][]string {
	type ms struct {
		mode  string
		score float64
	}
	items := make([]ms, 0, len(scores))
	for m, s := range scores {
		items = append(items, ms{m, s})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score < items[j].score
		}
		return items[i].mode < items[j].mode
	})

	var tiers [][]string
	var cur []string
	var tierFirst float64
	for i, it := range items {
		if i == 0 {
			tierFirst = it.score
			cur = []string{it.mode}
			continue
		}
		window := epsRel * tierFirst
		if epsAbs > window {
			window = epsAbs
		}
		if it.score-tierFirst <= window {
			cur = append(cur, it.mode)
		} else {
			tiers = append(tiers, cur)
			tierFirst = it.score
			cur = []string{it.mode}
		}
	}
	if len(cur) > 0 {
		tiers = append(tiers, cur)
	}
	return tiers
}

// Rank runs the weighted Schulze method over the ballots. epsRel/epsAbs
// control per-ballot tie tiering; w2d/w3d are the fixed total weights
// assigned to the "2d" and "3d" groups (each group's weight is split
// evenly across its ballots, so ballot count doesn't skew the group).
func Rank(bs []Ballot, epsRel, epsAbs, w2d, w3d float64) RankResult {
	res := RankResult{
		Pairwise:      map[string]map[string]float64{},
		Strength:      map[string]map[string]float64{},
		Wins:          map[string]int{},
		MeanRank:      map[string]float64{},
		EpsRel:        epsRel,
		EpsAbs:        epsAbs,
		GroupWeight2d: w2d,
		GroupWeight3d: w3d,
	}

	// Candidate set = union of all scored modes.
	candSet := map[string]bool{}
	for _, b := range bs {
		for m := range b.Scores {
			candSet[m] = true
		}
	}
	cands := make([]string, 0, len(candSet))
	for m := range candSet {
		cands = append(cands, m)
	}
	sort.Strings(cands)
	res.Candidates = cands

	// Count ballots per normalized group (unknown -> "2d").
	var n2d, n3d, nUnknown int
	for _, b := range bs {
		switch b.Group {
		case "3d":
			n3d++
		case "2d":
			n2d++
		default:
			nUnknown++
			n2d++ // unknown ballots weighted as 2d
		}
	}
	res.N2d, res.N3d, res.NUnknown = n2d, n3d, nUnknown
	if n2d > 0 {
		res.W2d = w2d / float64(n2d)
	}
	if n3d > 0 {
		res.W3d = w3d / float64(n3d)
	}

	// Initialize the pairwise matrix.
	for _, a := range cands {
		res.Pairwise[a] = map[string]float64{}
		for _, b := range cands {
			res.Pairwise[a][b] = 0
		}
	}

	// Accumulate weighted pairwise defeats and per-ballot tierings.
	rankSum := map[string]float64{}
	rankCount := map[string]int{}
	for _, b := range bs {
		group := b.Group
		weight := res.W2d
		if b.Group == "3d" {
			weight = res.W3d
		} else if b.Group != "2d" {
			group = "2d" // normalize unknown
		}
		tiers := tierScores(b.Scores, epsRel, epsAbs)
		res.Ballots = append(res.Ballots, BallotTiers{
			Voter:  b.Voter,
			Group:  group,
			Weight: weight,
			Tiers:  tiers,
		})
		// For every ordered pair of tiers (better, worse), the better
		// tier's modes defeat the worse tier's modes with this weight.
		for bi := 0; bi < len(tiers); bi++ {
			for _, mode := range tiers[bi] {
				rankSum[mode] += float64(bi + 1)
				rankCount[mode]++
			}
			for wi := bi + 1; wi < len(tiers); wi++ {
				for _, a := range tiers[bi] {
					for _, c := range tiers[wi] {
						res.Pairwise[a][c] += weight
					}
				}
			}
		}
	}
	for m, sum := range rankSum {
		res.MeanRank[m] = sum / float64(rankCount[m])
	}

	// Schulze widest-path strengths (winning-votes variant): an edge
	// a->b exists with strength d[a][b] only when d[a][b] > d[b][a].
	p := res.Strength
	for _, a := range cands {
		p[a] = map[string]float64{}
		for _, b := range cands {
			if a == b {
				continue
			}
			if res.Pairwise[a][b] > res.Pairwise[b][a] {
				p[a][b] = res.Pairwise[a][b]
			} else {
				p[a][b] = 0
			}
		}
	}
	for _, i := range cands {
		for _, j := range cands {
			if j == i {
				continue
			}
			for _, k := range cands {
				if k == i || k == j {
					continue
				}
				via := p[j][i]
				if p[i][k] < via {
					via = p[i][k]
				}
				if via > p[j][k] {
					p[j][k] = via
				}
			}
		}
	}

	// Win count = number of opponents a strictly beats on path strength.
	for _, a := range cands {
		wins := 0
		for _, b := range cands {
			if a == b {
				continue
			}
			if p[a][b] > p[b][a] {
				wins++
			}
		}
		res.Wins[a] = wins
	}

	// Order by descending win count; equal win counts tie.
	sorted := make([]string, len(cands))
	copy(sorted, cands)
	sort.Slice(sorted, func(i, j int) bool {
		if res.Wins[sorted[i]] != res.Wins[sorted[j]] {
			return res.Wins[sorted[i]] > res.Wins[sorted[j]]
		}
		return sorted[i] < sorted[j]
	})
	var order [][]string
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && res.Wins[sorted[j]] == res.Wins[sorted[i]] {
			j++
		}
		tier := make([]string, j-i)
		copy(tier, sorted[i:j])
		order = append(order, tier)
		i = j
	}
	res.Order = order

	return res
}
