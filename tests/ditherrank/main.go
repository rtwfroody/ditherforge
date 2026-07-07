// Command ditherrank computes a Condorcet (Schulze) ranking of dither
// modes from the perceptual ballots emitted by tests/ditherbench and
// tests/ditherrender.
//
// Each perceptual measurement is a ranked-ballot voter:
//   - ditherbench contributes one ballot per (fixture, pcp-scale),
//     group "2d".
//   - ditherrender contributes one ballot per (view, σ) using the mean
//     Lab ΔE, group "3d".
//
// Ballots are partial (they rank only the modes they scored) and are
// group-weighted so that the 2d and 3d groups each contribute a fixed
// total weight regardless of how many ballots they carry — by default
// the 3d group counts double (it exercises the real pipeline geometry).
// See package tests/ballots for the schema and the weighted-Schulze
// algorithm.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rtwfroody/ditherforge/tests/ballots"
)

func main() {
	epsRel := flag.Float64("eps-rel", 0.05, "relative epsilon for tie tiering within a ballot")
	epsAbs := flag.Float64("eps-abs", 0.1, "absolute epsilon (ΔE) for tie tiering within a ballot")
	w2d := flag.Float64("weight-2d", 1.0, "total group weight for 2d (ditherbench) ballots")
	w3d := flag.Float64("weight-3d", 2.0, "total group weight for 3d (ditherrender) ballots")
	verbose := flag.Bool("v", false, "print each ballot's tiered ranking and the full pairwise matrix")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ditherrank [flags] ballots.json [ballots.json ...]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	var all []ballots.Ballot
	for _, p := range paths {
		bs, err := ballots.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", p, err)
			os.Exit(1)
		}
		all = append(all, bs...)
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no ballots found")
		os.Exit(1)
	}

	res := ballots.Rank(all, *epsRel, *epsAbs, *w2d, *w3d)

	if *verbose {
		printBallots(res)
		printPairwise(res)
	}

	printRanking(res)
}

// printRanking emits the numbered ranking table plus a summary line.
func printRanking(res ballots.RankResult) {
	fmt.Println("Condorcet (Schulze) ranking of dither modes:")
	fmt.Printf("  %-4s %-40s %5s %9s\n", "rank", "mode(s)", "wins", "meanrank")
	rank := 1
	for _, tier := range res.Order {
		modes := make([]string, len(tier))
		copy(modes, tier)
		sort.Strings(modes)
		for i, m := range modes {
			label := ""
			if i == 0 {
				if len(modes) > 1 {
					label = fmt.Sprintf("%d (tied)", rank)
				} else {
					label = fmt.Sprintf("%d", rank)
				}
			}
			fmt.Printf("  %-4s %-40s %5d %9.2f\n", label, m, res.Wins[m], res.MeanRank[m])
		}
		rank += len(tier)
	}

	fmt.Printf("\n%d ballots (%d 2d @ w=%.4f, %d 3d @ w=%.4f",
		len(res.Ballots), res.N2d, res.W2d, res.N3d, res.W3d)
	if res.NUnknown > 0 {
		fmt.Printf("; %d ballots had an unknown group and were weighted as 2d", res.NUnknown)
	}
	fmt.Printf("), %d candidates, epsRel=%.3g epsAbs=%.3g, groupWeight 2d=%.3g 3d=%.3g\n",
		len(res.Candidates), res.EpsRel, res.EpsAbs, res.GroupWeight2d, res.GroupWeight3d)
}

// printBallots dumps each ballot's tiered ranking (verbose mode).
func printBallots(res ballots.RankResult) {
	fmt.Println("=== ballots (epsilon-tiered, best tier first) ===")
	for _, b := range res.Ballots {
		fmt.Printf("  %-40s [%s w=%.4f]\n", b.Voter, b.Group, b.Weight)
		for i, tier := range b.Tiers {
			fmt.Printf("      %2d: %s\n", i+1, strings.Join(tier, ", "))
		}
	}
	fmt.Println()
}

// printPairwise dumps the weighted pairwise-defeat matrix d[a][b]
// (verbose mode). Rows are "a", columns are "b"; the cell is the total
// weight of ballots ranking a strictly better than b.
func printPairwise(res ballots.RankResult) {
	fmt.Println("=== pairwise defeat matrix d[row][col] (weight ranking row better than col) ===")
	cands := res.Candidates
	// Header.
	fmt.Printf("  %-22s", "")
	for _, c := range cands {
		fmt.Printf(" %8s", abbrev(c))
	}
	fmt.Println()
	for _, a := range cands {
		fmt.Printf("  %-22s", a)
		for _, b := range cands {
			if a == b {
				fmt.Printf(" %8s", "-")
				continue
			}
			fmt.Printf(" %8.3f", res.Pairwise[a][b])
		}
		fmt.Println()
	}
	fmt.Println()
}

// abbrev shortens a mode name to fit the pairwise-matrix column width.
func abbrev(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
