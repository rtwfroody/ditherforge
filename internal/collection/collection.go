// Package collection manages named filament color collections.
// Built-in collections are embedded in the binary; user collections
// are stored as text files in the user's config directory.
package collection

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/palette"
)

// InventoryName is the reserved name for the user's inventory collection.
// It is auto-created with default colors and cannot be deleted.
const InventoryName = "Inventory"

//go:embed builtins/*.txt
var builtinFS embed.FS

// Collection is a named list of filament colors.
type Collection struct {
	Name    string
	Entries []palette.InventoryEntry
	BuiltIn bool
	Path    string // filesystem path; empty for built-in
}

// Manager loads and manages collections.
type Manager struct {
	builtins []Collection
	dir      string // user collections directory
}

// NewManager creates a Manager, loading built-in collections and
// creating the user collections directory if needed.
func NewManager() (*Manager, error) {
	m := &Manager{}

	// Load embedded built-ins.
	entries, err := builtinFS.ReadDir("builtins")
	if err != nil {
		return nil, fmt.Errorf("reading built-in collections: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := builtinFS.ReadFile("builtins/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading built-in %s: %w", e.Name(), err)
		}
		inv, err := palette.ParseInventoryData(data)
		if err != nil {
			return nil, fmt.Errorf("parsing built-in %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		name = strings.ReplaceAll(name, "_", " ")
		// Title-case each word.
		words := strings.Fields(name)
		for i, w := range words {
			if len(w) > 0 {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		name = strings.Join(words, " ")
		m.builtins = append(m.builtins, Collection{
			Name:    name,
			Entries: inv,
			BuiltIn: true,
		})
	}

	// Set up user collections directory.
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("finding config directory: %w", err)
	}
	m.dir = filepath.Join(configDir, "ditherforge", "collections")
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating collections directory: %w", err)
	}

	// Ensure the Inventory collection exists with default colors.
	if err := m.ensureInventory(); err != nil {
		return nil, fmt.Errorf("creating inventory collection: %w", err)
	}

	return m, nil
}

// ensureInventory creates the Inventory collection if it doesn't exist,
// populated with the 8 default colors.
func (m *Manager) ensureInventory() error {
	path := filepath.Join(m.dir, InventoryName+".txt")
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking inventory file: %w", err)
	}
	defaultColors := []string{
		"#00FFFF cyan",
		"#FF00FF magenta",
		"#FFFF00 yellow",
		"#000000 black",
		"#FFFFFF white",
		"#FF0000 red",
		"#008000 green",
		"#0000FF blue",
	}
	data := strings.Join(defaultColors, "\n") + "\n"
	return os.WriteFile(path, []byte(data), 0o644)
}

// List returns all collections (built-ins first, then user).
func (m *Manager) List() []Collection {
	result := make([]Collection, len(m.builtins))
	copy(result, m.builtins)

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(m.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		inv, err := palette.ParseInventoryData(data)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		result = append(result, Collection{
			Name:    name,
			Entries: inv,
			Path:    path,
		})
	}
	return result
}

// Get returns a collection by name.
func (m *Manager) Get(name string) (Collection, bool) {
	for _, c := range m.List() {
		if c.Name == name {
			return c, true
		}
	}
	return Collection{}, false
}

// Import copies an inventory file into the user collections directory
// and returns the resulting collection.
func (m *Manager) Import(path string) (Collection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Collection{}, err
	}
	inv, err := palette.ParseInventoryData(data)
	if err != nil {
		return Collection{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(inv) == 0 {
		return Collection{}, fmt.Errorf("%s contains no colors", path)
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	dest := filepath.Join(m.dir, base+".txt")

	// Avoid overwriting existing files.
	if _, err := os.Stat(dest); err == nil {
		for i := 2; ; i++ {
			dest = filepath.Join(m.dir, fmt.Sprintf("%s_%d.txt", base, i))
			if _, err := os.Stat(dest); os.IsNotExist(err) {
				break
			}
		}
	}

	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return Collection{}, err
	}

	name := strings.TrimSuffix(filepath.Base(dest), ".txt")
	return Collection{
		Name:    name,
		Entries: inv,
		Path:    dest,
	}, nil
}

// Delete removes a user collection. Returns an error for built-in or protected collections.
func (m *Manager) Delete(name string) error {
	if name == InventoryName {
		return fmt.Errorf("cannot delete the %s collection", InventoryName)
	}
	for _, b := range m.builtins {
		if b.Name == name {
			return fmt.Errorf("cannot delete built-in collection %q", name)
		}
	}
	path := filepath.Join(m.dir, name+".txt")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("collection %q not found", name)
	}
	return os.Remove(path)
}

// Rename renames a user collection. Returns an error for built-in or protected collections.
func (m *Manager) Rename(oldName, newName string) error {
	if oldName == InventoryName || newName == InventoryName {
		return fmt.Errorf("cannot rename the %s collection", InventoryName)
	}
	for _, b := range m.builtins {
		if b.Name == oldName {
			return fmt.Errorf("cannot rename built-in collection %q", oldName)
		}
	}
	oldPath := filepath.Join(m.dir, oldName+".txt")
	newPath := filepath.Join(m.dir, newName+".txt")
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return fmt.Errorf("collection %q not found", oldName)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("collection %q already exists", newName)
	}
	return os.Rename(oldPath, newPath)
}

// Save writes entries to an existing user collection, replacing all colors.
// Returns an error for built-in collections.
func (m *Manager) Save(name string, entries []palette.InventoryEntry) error {
	for _, b := range m.builtins {
		if b.Name == name {
			return fmt.Errorf("cannot modify built-in collection %q", name)
		}
	}
	path := filepath.Join(m.dir, name+".txt")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("collection %q not found", name)
	}
	return m.writeFile(path, entries)
}

// Create creates a new empty user collection file.
func (m *Manager) Create(name string, entries []palette.InventoryEntry) error {
	path := filepath.Join(m.dir, name+".txt")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("collection %q already exists", name)
	}
	return m.writeFile(path, entries)
}

// writeFile writes inventory entries to a file in "#RRGGBB Label" format.
func (m *Manager) writeFile(path string, entries []palette.InventoryEntry) error {
	var lines []string
	for _, e := range entries {
		line := fmt.Sprintf("#%02X%02X%02X", e.Color[0], e.Color[1], e.Color[2])
		if e.Label != "" {
			line += " " + e.Label
		}
		lines = append(lines, line)
	}
	data := strings.Join(lines, "\n")
	if len(lines) > 0 {
		data += "\n"
	}
	return os.WriteFile(path, []byte(data), 0o644)
}
