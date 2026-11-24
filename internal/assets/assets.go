// Package assets holds the support files gopgext needs to turn Go code into a
// PostgreSQL extension, and materializes them in the user's cache directory
// (with tinyguest/assets).
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
	"embed"
	"io/fs"
	"path/filepath"

	tgassets "github.com/jespino/tinyguest/assets"
	"github.com/jespino/tinyguest/toolchain"
)

//go:embed _files
var files embed.FS

// LibModule is the module path of the Go package user code imports.
const LibModule = "gopgext"

// LibGoVersion is the go directive of the generated lib go.mod.
const LibGoVersion = "1.23"

// Platform is the build tag of the runtime layer in overlay/.
const Platform = "pgext"

// excludedFiles are TinyGo source files (relative to the root) that would
// otherwise build for our target but conflict with the pgext runtime layer:
// baremetal_memory.go takes heap and stack bounds from linker-script symbols,
// which a shared library doesn't have (runtime_pgext.go provides them);
// rand_norng.go has no source of randomness (runtime_pgext.go seeds the
// runtime from pg_strong_random).
var excludedFiles = []string{
	"src/runtime/baremetal_memory.go",
	"src/runtime/rand_norng.go",
}

// Root is a materialized copy of the assets plus a TinyGo root overlay.
type Root struct {
	*tgassets.Root
	CSrc       string // C runtime sources
	Frameworks string // C glue of framework packages: frameworks/<pkg>/
}

// Prepare materializes the assets for the given TinyGo under cacheDir,
// reusing a previous copy when nothing changed.
func Prepare(cacheDir string, tg *toolchain.TinyGo) (*Root, error) {
	sub, err := fs.Sub(files, "_files")
	if err != nil {
		return nil, err
	}
	r, err := tgassets.Prepare(tgassets.Config{
		Files:        sub,
		Platform:     Platform,
		Exclude:      excludedFiles,
		LibModule:    LibModule,
		LibGoVersion: LibGoVersion,
		CacheDir:     cacheDir,
		TinyGo:       tg,
	})
	if err != nil {
		return nil, err
	}
	return &Root{
		Root:       r,
		CSrc:       filepath.Join(r.Dir, "csrc"),
		Frameworks: filepath.Join(r.Dir, "frameworks"),
	}, nil
}

// File returns the content of an embedded file (path relative to _files).
func File(name string) ([]byte, error) {
	return files.ReadFile("_files/" + name)
}
