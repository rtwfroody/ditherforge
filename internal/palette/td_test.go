package palette

import "testing"

func TestParseInventoryTD(t *testing.T) {
	data := []byte(`
#FF0000 Ruby Red
#FFFF00 td=4.3 Sunny Yellow
#00FF00 td=2.5
#0000FF td=oops Bad TD Label
`)
	entries, err := ParseInventoryData(data)
	if err != nil {
		t.Fatalf("ParseInventoryData: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	// No td= token: default opaque TD, label preserved.
	if entries[0].TD != DefaultTD {
		t.Errorf("entry0 TD = %v, want default %v", entries[0].TD, float32(DefaultTD))
	}
	if entries[0].Label != "Ruby Red" {
		t.Errorf("entry0 Label = %q, want %q", entries[0].Label, "Ruby Red")
	}

	// td= with a following label.
	if entries[1].TD != 4.3 {
		t.Errorf("entry1 TD = %v, want 4.3", entries[1].TD)
	}
	if entries[1].Label != "Sunny Yellow" {
		t.Errorf("entry1 Label = %q, want %q", entries[1].Label, "Sunny Yellow")
	}

	// td= with no label.
	if entries[2].TD != 2.5 || entries[2].Label != "" {
		t.Errorf("entry2 = {TD %v, Label %q}, want {2.5, \"\"}", entries[2].TD, entries[2].Label)
	}

	// Malformed td= value falls back to default and stays in the label.
	if entries[3].TD != DefaultTD {
		t.Errorf("entry3 TD = %v, want default %v", entries[3].TD, float32(DefaultTD))
	}
	if entries[3].Label != "td=oops Bad TD Label" {
		t.Errorf("entry3 Label = %q, want %q", entries[3].Label, "td=oops Bad TD Label")
	}
}
