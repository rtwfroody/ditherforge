package materialx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ResourceResolver opens files referenced by a MaterialX document — image
// inputs name them via relative paths, so the resolver decouples the
// document model from where the bytes physically live (filesystem
// directory, .zip archive, in-memory map for tests).
type ResourceResolver interface {
	Open(relpath string) (io.ReadCloser, error)
}

// ParsePackage opens a .mtlx file or a .zip archive containing one and
// returns a Document with Resolver populated, so image-backed graphs
// can find their referenced textures.
//
// .zip archives are expected to hold the .mtlx at the archive root (or
// in a single top-level directory) alongside any referenced texture
// files. Multiple .mtlx in the archive is an error — pick one.
func ParsePackage(path string) (*Document, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".mtlx":
		return ParseFile(path)
	case ".zip":
		return parseZipPackage(path)
	default:
		return nil, fmt.Errorf("materialx: unsupported package extension %q (want .mtlx or .zip)", ext)
	}
}

// ParseFileWithResolver is the same as ParseFile but lets the caller
// supply a custom resolver — useful for tests that mount in-memory
// fixtures.
func ParseFileWithResolver(path string, r ResourceResolver) (*Document, error) {
	doc, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	doc.Resolver = r
	return doc, nil
}

// dirResolver resolves paths relative to a filesystem directory.
type dirResolver struct {
	base string
}

func (d *dirResolver) Open(relpath string) (io.ReadCloser, error) {
	clean := filepath.FromSlash(filepath.Clean(relpath))
	if filepath.IsAbs(clean) {
		return nil, fmt.Errorf("materialx: refusing to resolve absolute path %q", relpath)
	}
	// Reject any path segment equal to ".." — this is robust on both
	// "/" (Linux/macOS) and "\" (Windows) separators, where a naive
	// HasPrefix("..") would let "..\foo" through on Windows after
	// FromSlash converts forward slashes but leaves the existing
	// backslashes alone.
	if slices.Contains(strings.Split(clean, string(filepath.Separator)), "..") {
		return nil, fmt.Errorf("materialx: refusing to resolve %q outside %q", relpath, d.base)
	}
	full := filepath.Join(d.base, clean)
	if f, err := os.Open(full); err == nil {
		return f, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// Permission denied, IO error, etc. — surface as-is rather
		// than masking it with a (potentially-incorrect) "not found".
		return nil, err
	}
	// Case-insensitive fallback for the same reason as the zip
	// resolver: real-world packs authored on macOS/Windows ship with
	// case mismatches between the .mtlx graph's filename inputs and
	// the on-disk texture filenames. Walk each path component, taking
	// the first case-insensitive match per directory; this stays
	// inside d.base and never escapes via "..".
	parts := strings.Split(clean, string(filepath.Separator))
	cur := d.base
	for _, part := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return nil, err
		}
		match := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				match = e.Name()
				break
			}
		}
		if match == "" {
			return nil, fmt.Errorf("materialx: %q not found in %q", relpath, d.base)
		}
		cur = filepath.Join(cur, match)
	}
	return os.Open(cur)
}

// zipResolver resolves paths against an opened *zip.Reader. Holds the
// underlying *os.File so callers can release the archive when done.
type zipResolver struct {
	r       *zip.Reader
	closer  io.Closer
	prefix  string // top-level directory inside the zip, if any
	entries map[string]*zip.File
}

func (z *zipResolver) Open(relpath string) (io.ReadCloser, error) {
	clean := path.Clean(strings.ReplaceAll(relpath, "\\", "/"))
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
		return nil, fmt.Errorf("materialx: refusing to resolve %q outside zip", relpath)
	}
	keys := []string{clean, path.Join(z.prefix, clean)}
	// Case-sensitive lookup first — exact matches always win.
	for _, key := range keys {
		if f, ok := z.entries[key]; ok {
			return f.Open()
		}
	}
	// Fallback: case-insensitive scan. Many real-world MaterialX
	// packs (Polyhaven, GPUOpen MatLib, AmbientCG, …) are authored on
	// macOS/Windows where the filesystem is case-insensitive by
	// default, so the .mtlx graph references e.g.
	// "textures/Foo_baseColor.png" while the zip happens to contain
	// "textures/Foo_basecolor.png". Linux users hit a hard miss
	// without this fallback. We do this only after the case-sensitive
	// pass so a pack that genuinely contains two same-named-different-
	// case files still resolves the exact one.
	for _, key := range keys {
		for entryName, f := range z.entries {
			if strings.EqualFold(entryName, key) {
				return f.Open()
			}
		}
	}
	return nil, fmt.Errorf("materialx: %q not found in zip", relpath)
}

// Close releases the underlying zip file handle. Safe to call multiple
// times.
func (z *zipResolver) Close() error {
	if z.closer == nil {
		return nil
	}
	c := z.closer
	z.closer = nil
	return c.Close()
}

func parseZipPackage(zipPath string) (*Document, error) {
	f, err := os.Open(zipPath)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zr, err := zip.NewReader(f, stat.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("materialx: open zip: %w", err)
	}

	var mtlxFile *zip.File
	entries := make(map[string]*zip.File, len(zr.File))
	for _, e := range zr.File {
		if e.FileInfo().IsDir() {
			continue
		}
		entries[e.Name] = e
		if strings.EqualFold(filepath.Ext(e.Name), ".mtlx") {
			if mtlxFile != nil {
				f.Close()
				return nil, fmt.Errorf("materialx: zip contains multiple .mtlx files (%q and %q)",
					mtlxFile.Name, e.Name)
			}
			mtlxFile = e
		}
	}
	if mtlxFile == nil {
		f.Close()
		return nil, errors.New("materialx: zip contains no .mtlx file")
	}

	// All other resources resolve relative to the .mtlx's containing
	// directory inside the archive — same convention as a directory layout.
	prefix := path.Dir(mtlxFile.Name)
	if prefix == "." {
		prefix = ""
	}

	rc, err := mtlxFile.Open()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("materialx: read %q: %w", mtlxFile.Name, err)
	}
	doc, err := Parse(rc)
	rc.Close()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("materialx: parse %q: %w", mtlxFile.Name, err)
	}

	doc.Resolver = &zipResolver{
		r:       zr,
		closer:  f,
		prefix:  prefix,
		entries: entries,
	}
	return doc, nil
}

// mapResolver is an in-memory ResourceResolver used by tests. Public so
// external test packages can construct fixtures.
type mapResolver map[string][]byte

func (m mapResolver) Open(relpath string) (io.ReadCloser, error) {
	clean := path.Clean(strings.ReplaceAll(relpath, "\\", "/"))
	b, ok := m[clean]
	if !ok {
		return nil, fmt.Errorf("materialx: %q not found in map resolver", relpath)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// NewMapResolver returns an in-memory ResourceResolver backed by the
// given path-to-bytes map. Test-only.
func NewMapResolver(files map[string][]byte) ResourceResolver { return mapResolver(files) }
