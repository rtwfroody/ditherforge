package ballots

import (
	"reflect"
	"testing"
)

// tiersEqual compares two tierings for exact equality (order matters
// both across tiers and within each tier).
func tiersEqual(a, b [][]string) bool {
	return reflect.DeepEqual(a, b)
}

func TestTierScores_AbsFloor(t *testing.T) {
	// With epsAbs=0.1 and epsRel=0, scores within 0.1 of the tier's
	// first score group together. 1.00 and 1.05 are within 0.1 of 1.00;
	// 1.30 opens a new tier.
	scores := map[string]float64{"a": 1.00, "b": 1.05, "c": 1.30}
	got := tierScores(scores, 0.0, 0.1)
	want := [][]string{{"a", "b"}, {"c"}}
	if !tiersEqual(got, want) {
		t.Fatalf("abs floor tiering: got %v, want %v", got, want)
	}
}

func TestTierScores_RelWindowAndAnchor(t *testing.T) {
	// epsRel=0.05, epsAbs=0. Tier anchored on the FIRST score (1.00), so
	// the window is 0.05. 1.04 joins (0.04 <= 0.05). 1.09 is 0.09 from
	// the anchor 1.00 (> 0.05) so it opens a new tier — even though it is
	// only 0.05 from 1.04. This verifies tier-first anchoring, not
	// pairwise chaining.
	scores := map[string]float64{"a": 1.00, "b": 1.04, "c": 1.09}
	got := tierScores(scores, 0.05, 0.0)
	want := [][]string{{"a", "b"}, {"c"}}
	if !tiersEqual(got, want) {
		t.Fatalf("rel window tiering: got %v, want %v", got, want)
	}
}

func TestTierScores_SingleTierWhenAllClose(t *testing.T) {
	scores := map[string]float64{"a": 2.00, "b": 2.05, "c": 2.09}
	got := tierScores(scores, 0.0, 0.1)
	want := [][]string{{"a", "b", "c"}}
	if !tiersEqual(got, want) {
		t.Fatalf("single tier: got %v, want %v", got, want)
	}
}

// orderFlat flattens a RankResult order into a single slice of modes,
// best first, for convenient assertions when there are no ties.
func orderFlat(res RankResult) []string {
	var out []string
	for _, tier := range res.Order {
		out = append(out, tier...)
	}
	return out
}

func TestRank_CondorcetWinner(t *testing.T) {
	// Three ballots, all agree a < b < c (lower is better), so "a" is a
	// clear Condorcet winner and "c" is last. Use tiny scores far apart
	// so no epsilon tiering merges them.
	bs := []Ballot{
		{Voter: "v1", Group: "2d", Scores: map[string]float64{"a": 1, "b": 5, "c": 9}},
		{Voter: "v2", Group: "2d", Scores: map[string]float64{"a": 2, "b": 6, "c": 10}},
		{Voter: "v3", Group: "2d", Scores: map[string]float64{"a": 1, "b": 4, "c": 8}},
	}
	res := Rank(bs, 0.0, 0.0, 1.0, 2.0)
	got := orderFlat(res)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("condorcet winner order: got %v, want %v", got, want)
	}
	if res.Wins["a"] != 2 {
		t.Errorf("winner should beat both others: wins[a]=%d", res.Wins["a"])
	}
	if res.Wins["c"] != 0 {
		t.Errorf("loser should beat nobody: wins[c]=%d", res.Wins["c"])
	}
}

func TestRank_Cycle(t *testing.T) {
	// Textbook Condorcet cycle over {a,b,c}: a>b, b>c, c>a pairwise (here
	// "better" = lower score). Construct with three ballots:
	//   v1: a < b < c   (a>b, b>c, a>c)
	//   v2: b < c < a   (b>c, c>a, b>a)
	//   v3: c < a < b   (c>a, a>b, c>b)
	// Pairwise majorities: a>b (v1,v3), b>c (v1,v2), c>a (v2,v3) — a
	// 3-cycle. Schulze must still produce a total order without panicking
	// or leaving a NaN; with equal-strength edges all three tie on win
	// count.
	bs := []Ballot{
		{Voter: "v1", Group: "2d", Scores: map[string]float64{"a": 1, "b": 2, "c": 3}},
		{Voter: "v2", Group: "2d", Scores: map[string]float64{"b": 1, "c": 2, "a": 3}},
		{Voter: "v3", Group: "2d", Scores: map[string]float64{"c": 1, "a": 2, "b": 3}},
	}
	res := Rank(bs, 0.0, 0.0, 1.0, 2.0)
	// Every candidate should appear exactly once.
	seen := map[string]int{}
	for _, tier := range res.Order {
		for _, m := range tier {
			seen[m]++
		}
	}
	for _, m := range []string{"a", "b", "c"} {
		if seen[m] != 1 {
			t.Fatalf("candidate %q appears %d times in order %v", m, seen[m], res.Order)
		}
	}
	// By symmetry the beatpaths are all equal strength, so all three tie
	// on win count in a single tier.
	if len(res.Order) != 1 || len(res.Order[0]) != 3 {
		t.Errorf("symmetric cycle should yield one 3-way tie, got %v", res.Order)
	}
}

func TestRank_GroupWeightingLetsOneBallotOutvoteTwo(t *testing.T) {
	// Two 2d ballots prefer a < b; one 3d ballot prefers b < a. With
	// group weights 2d=1.0 (split across 2 ballots -> 0.5 each) and
	// 3d=2.0 (one ballot -> 2.0), the single 3d ballot outweighs both 2d
	// ballots combined (2.0 > 1.0), so b wins.
	bs := []Ballot{
		{Voter: "d1", Group: "2d", Scores: map[string]float64{"a": 1, "b": 2}},
		{Voter: "d2", Group: "2d", Scores: map[string]float64{"a": 1, "b": 2}},
		{Voter: "r1", Group: "3d", Scores: map[string]float64{"b": 1, "a": 2}},
	}
	res := Rank(bs, 0.0, 0.0, 1.0, 2.0)
	got := orderFlat(res)
	want := []string{"b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("3d group should outvote 2d: got %v, want %v", got, want)
	}
	// d[b][a] = 2.0 (one 3d ballot), d[a][b] = 1.0 (two 2d @ 0.5).
	if got := res.Pairwise["b"]["a"]; got != 2.0 {
		t.Errorf("d[b][a] = %v, want 2.0", got)
	}
	if got := res.Pairwise["a"]["b"]; got != 1.0 {
		t.Errorf("d[a][b] = %v, want 1.0", got)
	}
}

func TestRank_PartialBallotsDoNotRankMissingLast(t *testing.T) {
	// Construct a case where treating a missing mode as ranked-last would
	// flip the result, and assert it does NOT flip.
	//
	// Candidates: a, b, and x. Many ballots score only {a, b} and mildly
	// prefer b < a. One ballot scores only {a, x}. Mode x is absent from
	// almost every ballot.
	//
	// If missing were treated as ranked-last, every {a,b} ballot would
	// also rank a and b BOTH ahead of x, piling huge defeats onto x and
	// making x last — and, more importantly for the flip, those ballots
	// would still say nothing false about a vs b. The dangerous flip is
	// on the a-vs-x and b-vs-x pairs: with missing-as-last, b would beat
	// x on every {a,b} ballot (many votes), but b and x never actually
	// co-occur on any ballot, so a correct partial-ballot method must
	// leave d[b][x] = d[x][b] = 0.
	bs := []Ballot{
		{Voter: "v1", Group: "2d", Scores: map[string]float64{"a": 2, "b": 1}},
		{Voter: "v2", Group: "2d", Scores: map[string]float64{"a": 2, "b": 1}},
		{Voter: "v3", Group: "2d", Scores: map[string]float64{"a": 2, "b": 1}},
		// x only ever appears here, and loses to a on this single ballot.
		{Voter: "v4", Group: "2d", Scores: map[string]float64{"a": 1, "x": 2}},
	}
	res := Rank(bs, 0.0, 0.0, 1.0, 2.0)

	// b and x never co-occur: both pairwise cells must be exactly 0.
	if res.Pairwise["b"]["x"] != 0 {
		t.Errorf("b and x never co-occur, d[b][x] should be 0, got %v", res.Pairwise["b"]["x"])
	}
	if res.Pairwise["x"]["b"] != 0 {
		t.Errorf("b and x never co-occur, d[x][b] should be 0, got %v", res.Pairwise["x"]["b"])
	}
	// a beats x on the one ballot they share.
	if res.Pairwise["a"]["x"] <= 0 {
		t.Errorf("a should defeat x on their shared ballot, d[a][x]=%v", res.Pairwise["a"]["x"])
	}
	// b beats a across three shared ballots.
	if res.Pairwise["b"]["a"] <= res.Pairwise["a"]["b"] {
		t.Errorf("b should defeat a, d[b][a]=%v d[a][b]=%v", res.Pairwise["b"]["a"], res.Pairwise["a"]["b"])
	}
}
