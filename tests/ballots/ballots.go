// Package ballots defines the shared schema for perceptual "ranked
// ballots" emitted by the dither research benches (tests/ditherbench
// and tests/ditherrender) and consumed by the Condorcet ranker
// (tests/ditherrank).
//
// Each Ballot is one perceptual measurement acting as a single voter
// in a weighted Schulze election over dither modes. Scores are
// lower-is-better perceptual ΔE numbers (mean Lab ΔE after a
// mask-normalized linear-light Gaussian blur). Different ballots may
// cover different subsets of candidate modes — a bench run restricted
// with -fixture/-mode, or a mode that errored on one fixture, simply
// omits those modes from Scores. Such ballots are PARTIAL: they
// express preferences only among the modes they actually scored and
// abstain on every pair involving a mode they did not score. Missing
// is NOT treated as ranked-last.
package ballots

import (
	"encoding/json"
	"fmt"
	"os"
)

// Ballot is one perceptual measurement's ranked preference over dither
// modes. Lower Scores are better (they are ΔE distances from a
// continuous-color reference), so the mode with the smallest score is
// this voter's first choice.
type Ballot struct {
	// Voter is a stable, human-readable identifier for the measurement,
	// e.g. "bench/uniform_terracotta/pcp8" or
	// "render/earth/front/0.8mm".
	Voter string `json:"voter"`
	// Group is the ballot's weighting bucket: "2d" (ditherbench) or
	// "3d" (ditherrender). The ranker normalizes each group's total
	// weight independently.
	Group string `json:"group"`
	// Scores maps mode name -> lower-is-better perceptual ΔE. A mode
	// absent from this map is not scored by this ballot; the ranker
	// abstains on every pair involving it.
	Scores map[string]float64 `json:"scores"`
}

// WriteFile serializes ballots to path as indented JSON.
func WriteFile(path string, bs []Ballot) error {
	data, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling ballots: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ReadFile parses a JSON ballot file previously written by WriteFile.
func ReadFile(path string) ([]Ballot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var bs []Ballot
	if err := json.Unmarshal(data, &bs); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return bs, nil
}
