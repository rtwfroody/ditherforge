package pipeline

import (
	"crypto/sha256"
	"fmt"
	"reflect"
)

// ConfigHash returns a SHA-256 hash of all Options fields that affect the
// voxelization cache. The app version is included so cache is invalidated on
// upgrades.
func ConfigHash(opts Options) [32]byte {
	h := sha256.New()
	fmt.Fprintf(h, "version=%s\n", Version)

	v := reflect.ValueOf(opts)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name := field.Name
		// Skip fields that don't affect voxelization output.
		switch name {
		case "Palette", "AutoPalette", "Output", "InventoryFile", "Inventory",
			"Dither", "NoMerge", "NoSimplify", "Force", "Stats", "ColorSnap", "NoCache":
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				fmt.Fprintf(h, "%s=<nil>\n", name)
			} else {
				fmt.Fprintf(h, "%s=%v\n", name, fv.Elem().Interface())
			}
		} else {
			fmt.Fprintf(h, "%s=%v\n", name, fv.Interface())
		}
	}

	var hash [32]byte
	copy(hash[:], h.Sum(nil))
	return hash
}
