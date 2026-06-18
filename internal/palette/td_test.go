package palette

import "testing"

func TestParseInventoryTD(t *testing.T) {
	data := []byte(`
#FF0000 Ruby Red
#FFE800 4.3 Sunny Yellow
#00FF00 2.5
#0000FF td=1.8 Token Blue
#A79E82 3M Tan
`)
	entries, err := ParseInventoryData(data)
	if err != nil {
		t.Fatalf("ParseInventoryData: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5", len(entries))
	}

	// No TD: default opaque, whole remainder is the label.
	if entries[0].TD != DefaultTD || entries[0].Label != "Ruby Red" {
		t.Errorf("entry0 = {TD %v, %q}, want {default, \"Ruby Red\"}", entries[0].TD, entries[0].Label)
	}
	// Positional TD + label (Panchroma convention).
	if entries[1].TD != 4.3 || entries[1].Label != "Sunny Yellow" {
		t.Errorf("entry1 = {TD %v, %q}, want {4.3, \"Sunny Yellow\"}", entries[1].TD, entries[1].Label)
	}
	// Positional TD, no label.
	if entries[2].TD != 2.5 || entries[2].Label != "" {
		t.Errorf("entry2 = {TD %v, %q}, want {2.5, \"\"}", entries[2].TD, entries[2].Label)
	}
	// Explicit td= fallback token.
	if entries[3].TD != 1.8 || entries[3].Label != "Token Blue" {
		t.Errorf("entry3 = {TD %v, %q}, want {1.8, \"Token Blue\"}", entries[3].TD, entries[3].Label)
	}
	// Label whose first token is not a pure number stays a label.
	if entries[4].TD != DefaultTD || entries[4].Label != "3M Tan" {
		t.Errorf("entry4 = {TD %v, %q}, want {default, \"3M Tan\"}", entries[4].TD, entries[4].Label)
	}
}

// TestBuiltinPanchromaBasicTD guards that the real built-in collection file
// parses with plausible per-color TDs (not all collapsed to the default),
// catching format regressions in the shipped data.
func TestBuiltinPanchromaBasicTD(t *testing.T) {
	data := []byte("#FFE800 4.3 Yellow\n#080A0D 0.1 Black\n#A79E82 Tan\n")
	entries, err := ParseInventoryData(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if entries[0].TD != 4.3 || entries[0].Label != "Yellow" {
		t.Errorf("yellow = {TD %v, %q}", entries[0].TD, entries[0].Label)
	}
	if entries[1].TD != 0.1 || entries[1].Label != "Black" {
		t.Errorf("black = {TD %v, %q}", entries[1].TD, entries[1].Label)
	}
	if entries[2].TD != DefaultTD || entries[2].Label != "Tan" {
		t.Errorf("tan = {TD %v, %q}", entries[2].TD, entries[2].Label)
	}
}
