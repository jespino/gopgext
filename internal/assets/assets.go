// Package assets holds the support files gopgext needs to turn Go code into a
// PostgreSQL extension, and materializes them in the user's cache directory.
//
// _files/ layout:
//
//	target.json   TinyGo target for code running inside a PostgreSQL backend
//	overlay/      files added on top of TinyGo's source tree (runtime platform)
//	lib/          the "gopgext" Go module: gopgext/pg, gopgext/fmgr
//	csrc/         C runtime compiled into every extension by PGXS
//	frameworks/   C glue of framework packages: frameworks/fdw/ is compiled in
//	              when the project imports gopgext/fdw (plus optional
//	              ldflags and install.sql files)
//
// The directory name starts with "_" so the go tool doesn't treat the Go files
// in it (which only build with TinyGo for the gopgext target) as packages of
// this module.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed _files
var files embed.FS

// LibModule is the module path of the Go package user code imports.
const LibModule = "gopgext"

// LibGoVersion is the go directive of the generated lib go.mod.
const LibGoVersion = "1.23"

// Root is a materialized copy of the assets plus a TinyGo root overlay.
type Root struct {
	Dir        string // root of this cache entry
	Target     string // target JSON path
	TinyGoRoot string // overlay TINYGOROOT
	Lib        string // module dir providing gopgext/pg
	CSrc       string // C runtime sources
	Frameworks string // C glue of framework packages: frameworks/<pkg>/
}

// Prepare materializes the assets for the given system TinyGo root/version
// under cacheDir, reusing a previous copy when nothing changed.
func Prepare(cacheDir, sysTinyGoRoot, tinygoVersion string) (*Root, error) {
	key, err := hash(sysTinyGoRoot, tinygoVersion)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, "root-"+key)
	r := &Root{
		Dir:        dir,
		Target:     filepath.Join(dir, "target.json"),
		TinyGoRoot: filepath.Join(dir, "tinygoroot"),
		Lib:        filepath.Join(dir, "lib"),
		CSrc:       filepath.Join(dir, "csrc"),
		Frameworks: filepath.Join(dir, "frameworks"),
	}
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return r, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(cacheDir, "tmp-root-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	if err := writeEmbedded(tmp); err != nil {
		return nil, err
	}
	goMod := fmt.Sprintf("module %s\n\ngo %s\n", LibModule, LibGoVersion)
	if err := os.WriteFile(filepath.Join(tmp, "lib", "go.mod"), []byte(goMod), 0o644); err != nil {
		return nil, err
	}
	if err := mirrorTinyGoRoot(sysTinyGoRoot, filepath.Join(tmp, "tinygoroot"), filepath.Join(tmp, "overlay")); err != nil {
		return nil, fmt.Errorf("creating TinyGo root overlay: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return nil, err
	}

	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		// Lost a race with a concurrent gopgext: use theirs.
		if _, statErr := os.Stat(filepath.Join(dir, ".complete")); statErr == nil {
			return r, nil
		}
		return nil, err
	}
	return r, nil
}

// File returns the content of an embedded file (path relative to _files).
func File(name string) ([]byte, error) {
	return files.ReadFile("_files/" + name)
}

func writeEmbedded(dst string) error {
	sub, err := fs.Sub(files, "_files")
	if err != nil {
		return err
	}
	return fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, p)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// mirrorTinyGoRoot creates dst as a mirror of the system TinyGo root: real
// directories under src/ with symlinked files (TinyGo's GOROOT merging needs
// real directories), other top-level entries symlinked, and the overlay files
// copied on top.
func mirrorTinyGoRoot(sys, dst, overlay string) error {
	entries, err := os.ReadDir(sys)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "src" {
			continue
		}
		if err := os.Symlink(filepath.Join(sys, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	srcRoot := filepath.Join(sys, "src")
	err = filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, p)
		out := filepath.Join(dst, "src", rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		return os.Symlink(p, out)
	})
	if err != nil {
		return err
	}
	for _, rel := range excludedFiles {
		if err := excludeFromTarget(filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	return filepath.WalkDir(overlay, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(overlay, p)
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		os.Remove(out)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// excludedFiles are TinyGo source files (relative to the root) that would
// otherwise build for our target but conflict with the pgext runtime layer:
// baremetal_memory.go takes heap and stack bounds from linker-script symbols,
// which a shared library doesn't have (runtime_pgext.go provides them).
var excludedFiles = []string{
	"src/runtime/baremetal_memory.go",
}

// excludeFromTarget replaces the symlinked file at p with a copy whose
// //go:build constraint also requires !pgext.
func excludeFromTarget(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if !strings.HasPrefix(lines[0], "//go:build ") || len(lines) != 2 {
		return fmt.Errorf("%s: expected a //go:build line first", p)
	}
	expr := strings.TrimPrefix(lines[0], "//go:build ")
	patched := fmt.Sprintf("//go:build (%s) && !pgext\n%s", expr, lines[1])
	if err := os.Remove(p); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(patched), 0o644)
}

// hash identifies a cache entry: embedded assets + the TinyGo installation.
func hash(sysTinyGoRoot, tinygoVersion string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", sysTinyGoRoot, tinygoVersion, LibGoVersion)
	var paths []string
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, p := range paths {
		data, err := files.ReadFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
