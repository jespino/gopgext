// Package build turns a Go extension project into a PostgreSQL extension:
// a shared library, a control file and an install script.
//
// Pipeline:
//
//	project *.go + generated wrappers ──tinygo build (custom target, overlay TINYGOROOT)──▶ go.raw.o
//	go.raw.o ──objcopy (strip debug, localize Go symbols)──▶ ext/go.o
//	ext/{runtime.c, spi.c, functions.c, go.o} ──PGXS (make)──▶ <name>.so
//	function signatures ──▶ <name>.control, <name>--<version>.sql
package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopgext/internal/assets"
)

// Options configure a build.
type Options struct {
	Dir      string   // project directory (contains go.mod)
	OutDir   string   // where to write the .so, .control and .sql; default Dir
	Name     string   // extension name; default derived from the go.mod module path
	Version  string   // extension version; default DefaultVersion
	Comment  string   // control file comment
	Trusted  bool     // control file: trusted (non-superusers with CREATE can install it)
	Requires []string // control file: extensions this one depends on (CREATE EXTENSION ... CASCADE installs them)
	PGConfig string   // pg_config to build against; default $PG_CONFIG or pg_config in PATH
	HeapSize string   // initial Go heap size; default DefaultHeapSize
	HeapMax  string   // address space reserved for the Go heap; default DefaultHeapMax
	Verbose  bool     // print commands and their full output
	Log      io.Writer
}

// Result describes a finished build.
type Result struct {
	Name      string
	Version   string
	Library   string   // <OutDir>/<name>.so
	Control   string   // <OutDir>/<name>.control
	Script    string   // <OutDir>/<name>--<version>.sql
	DataFiles []string // the project's tsearch_data/ files, for PGConfig.TSearchDir
	Functions []string
	PG        *PGConfig
	WorkDir   string
}

const (
	DefaultVersion  = "1.0"
	DefaultHeapSize = "1M"
	DefaultHeapMax  = "1G"
)

var heapSizeRe = regexp.MustCompile(`^[0-9]+[KMG]?$`)

// Build runs the full pipeline.
func Build(opts Options) (*Result, error) {
	if opts.Log == nil {
		opts.Log = os.Stderr
	}
	b := &builder{opts: opts}
	return b.run()
}

type builder struct {
	opts       Options
	ldflags    []string // from //gopgext:ldflags and frameworks
	aliases    []Alias  // from //gopgext:alias
	frameworks []string // imported gopgext packages with C glue
	fwSQL      [][]byte // install SQL of the frameworks
}

func (b *builder) step(format string, args ...any) {
	fmt.Fprintf(b.opts.Log, "gopgext: "+format+"\n", args...)
}

func (b *builder) run() (*Result, error) {
	proj, err := filepath.Abs(b.opts.Dir)
	if err != nil {
		return nil, err
	}
	mod, err := ReadGoMod(filepath.Join(proj, "go.mod"))
	if err != nil {
		return nil, err
	}
	name := b.opts.Name
	if name == "" {
		name = NameFromPath(mod.Path)
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	version := b.opts.Version
	if version == "" {
		version = DefaultVersion
	}
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("invalid extension version %q: use letters, digits, '.', '_' (no '--')", version)
	}
	for _, r := range b.opts.Requires {
		if !sqlNameRe.MatchString(r) || r == name {
			return nil, fmt.Errorf("invalid required extension %q", r)
		}
	}
	if b.opts.HeapSize == "" {
		b.opts.HeapSize = DefaultHeapSize
	}
	if b.opts.HeapMax == "" {
		b.opts.HeapMax = DefaultHeapMax
	}
	for _, v := range []string{b.opts.HeapSize, b.opts.HeapMax} {
		if !heapSizeRe.MatchString(v) {
			return nil, fmt.Errorf("invalid heap size %q: use bytes or a K/M/G suffix, e.g. 4M", v)
		}
	}
	heapSize, heapMax := parseSize(b.opts.HeapSize), parseSize(b.opts.HeapMax)
	if heapSize < 64<<10 || heapMax < heapSize {
		return nil, fmt.Errorf("heap size must be at least 64K and no more than the heap max (%s)", b.opts.HeapMax)
	}

	pg, err := FindPGConfig(b.opts.PGConfig)
	if err != nil {
		return nil, err
	}
	for _, tool := range []string{"tinygo", "objcopy", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s not found in PATH", tool)
		}
	}

	cache, err := CacheDir()
	if err != nil {
		return nil, err
	}
	tgRoot, tgVersion, err := tinygoInfo()
	if err != nil {
		return nil, err
	}
	root, err := assets.Prepare(cache, tgRoot, tgVersion)
	if err != nil {
		return nil, err
	}

	work := filepath.Join(cache, "build", shortHash(proj+"\x00"+pg.Path))
	ext := filepath.Join(work, "ext")
	if err := os.RemoveAll(ext); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(ext, 0o755); err != nil {
		return nil, err
	}

	// The project is built from a copy, plus the generated wrappers.
	src := filepath.Join(work, "src")
	pkg, err := prepareSource(proj, src)
	if err != nil {
		return nil, err
	}
	funcs := pkg.Funcs
	if b.ldflags, err = scanLDFlags(proj); err != nil {
		return nil, err
	}
	if b.aliases, err = scanAliases(proj); err != nil {
		return nil, err
	}
	goSrc, err := genGo(pkg)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(src, GeneratedGoFile), goSrc, 0o644); err != nil {
		return nil, err
	}
	goWork := filepath.Join(work, "go.work")
	if err := os.WriteFile(goWork, []byte(GoWorkContent(mod.GoVersion, src, root.Lib, false)), 0o644); err != nil {
		return nil, err
	}
	RefreshProjectGoWork(proj, mod.GoVersion, root.Lib)

	// 1. Go -> object.
	b.step("compiling Go code with TinyGo %s (%d SQL functions)", shortTinyGoVersion(tgVersion), len(funcs))
	rawObj := filepath.Join(work, "go.raw.o")
	cmd := exec.Command("tinygo", "build", "-target="+root.Target, "-opt=2", "-no-debug", "-o", rawObj, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "TINYGOROOT="+root.TinyGoRoot, "GOWORK="+goWork)
	if err := b.exec(cmd); err != nil {
		// Report positions in the project, not in the copy.
		return nil, errors.New(strings.ReplaceAll(err.Error(), src+string(filepath.Separator), proj+string(filepath.Separator)))
	}
	if b.frameworks, err = b.findFrameworks(root, src, cmd.Env); err != nil {
		return nil, err
	}

	// 2. Strip debug info and make everything but the entry points the C
	// runtime calls local: Go internals (runtime.alloc, main.main, ...) and
	// the libc functions TinyGo's baremetal runtime defines (malloc, ...)
	// must not clash with the backend's or another extension's symbols.
	keep := filepath.Join(work, "keep.txt")
	if err := os.WriteFile(keep, []byte(strings.Join(keptSymbols, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	if err := b.exec(exec.Command("objcopy", "--strip-debug", "--wildcard",
		"--keep-global-symbols="+keep, rawObj, filepath.Join(ext, "go.o"))); err != nil {
		return nil, err
	}

	// 3. PGXS builds the C runtime and links the library.
	if err := b.writeExt(root, proj, ext, name, version, heapSize, heapMax, pg, funcs); err != nil {
		return nil, err
	}
	b.step("building %s.so against PostgreSQL %s", name, pg.Version)
	if err := b.exec(exec.Command("make", "-C", ext, "with_llvm=no")); err != nil {
		return nil, err
	}

	// 4. Outputs.
	out := b.opts.OutDir
	if out == "" {
		out = proj
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	res := &Result{
		Name:    name,
		Version: version,
		Library: filepath.Join(out, name+".so"),
		Control: filepath.Join(out, name+".control"),
		Script:  filepath.Join(out, name+"--"+version+".sql"),
		PG:      pg,
		WorkDir: work,
	}
	for _, f := range funcs {
		res.Functions = append(res.Functions, f.SQLName)
	}
	if err := copyFile(filepath.Join(ext, name+".so"), res.Library, 0o755); err != nil {
		return nil, err
	}
	comment := b.opts.Comment
	if comment == "" {
		comment = mod.Path + " (Go extension built with gopgext)"
	}
	if err := os.WriteFile(res.Control, genControl(name, version, comment, b.opts.Trusted, b.opts.Requires), 0o644); err != nil {
		return nil, err
	}
	script, err := genSQL(name, mod.Path, pkg, b.fwSQL, filepath.Join(proj, SQLDir))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(res.Script, script, 0o644); err != nil {
		return nil, err
	}
	if res.DataFiles, err = DataFiles(filepath.Join(proj, TSearchDataDir)); err != nil {
		return nil, err
	}
	return res, nil
}

// TSearchDataDir is the project subdirectory whose files (text search
// dictionaries' rules, synonyms, stop words...) install into
// $SHAREDIR/tsearch_data.
const TSearchDataDir = "tsearch_data"

// DataFiles returns the regular files of dir, sorted (none if it doesn't
// exist).
func DataFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files, nil
}

// v1Re finds the SQL-callable functions of C sources: the library exports
// them too.
var v1Re = regexp.MustCompile(`(?m)^\s*PG_FUNCTION_INFO_V1\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)

// LDFlagsPragma adds linker flags for the library, e.g. to link a system
// library that csrc/ code uses:
//
//	//gopgext:ldflags -lcurl
const LDFlagsPragma = "//gopgext:ldflags"

// scanLDFlags collects the //gopgext:ldflags of the project's main package.
func scanLDFlags(proj string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(proj, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var flags []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), LDFlagsPragma)
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			for _, fl := range strings.Fields(rest) {
				if !strings.HasPrefix(fl, "-") || strings.ContainsAny(fl, "$`\\'\"") {
					return nil, fmt.Errorf("%s:%d: %s: invalid flag %q", f, i+1, LDFlagsPragma, fl)
				}
				flags = append(flags, fl)
			}
		}
	}
	return flags, nil
}

// AliasPragma exports a V1 function of the library under another C
// symbol, e.g. a framework's support function under the name an existing
// install script uses:
//
//	//gopgext:alias g_cube_consistent=gopgext_gist_consistent
const AliasPragma = "//gopgext:alias"

// Alias is a C symbol (Name) for the V1 function Target.
type Alias struct{ Name, Target string }

// scanAliases collects the //gopgext:alias of the project's main package.
func scanAliases(proj string) ([]Alias, error) {
	files, err := filepath.Glob(filepath.Join(proj, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var aliases []Alias
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), AliasPragma)
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			for _, a := range strings.Fields(rest) {
				name, target, _ := strings.Cut(a, "=")
				if !identRe.MatchString(name) || !identRe.MatchString(target) || seen[name] || reservedSymbol(name) != "" {
					return nil, fmt.Errorf("%s:%d: %s: invalid alias %q (use name=target, C identifiers, each name once, not gopgext_...)", f, i+1, AliasPragma, a)
				}
				seen[name] = true
				aliases = append(aliases, Alias{name, target})
			}
		}
	}
	return aliases, nil
}

// genAliases generates the V1 functions of aliases (appended to the
// generated functions.c).
func genAliases(aliases []Alias) []byte {
	var b bytes.Buffer
	for _, a := range aliases {
		fmt.Fprintf(&b, `
/* %[1]s: %[2]s (//gopgext:alias) */
extern Datum %[2]s(PG_FUNCTION_ARGS);
PG_FUNCTION_INFO_V1(%[1]s);
Datum
%[1]s(PG_FUNCTION_ARGS)
{
	return %[2]s(fcinfo);
}
`, a.Name, a.Target)
	}
	return b.Bytes()
}

// findFrameworks returns the gopgext framework packages (those with C glue
// in the assets) the project imports, directly or not.
func (b *builder) findFrameworks(root *assets.Root, dir string, env []string) ([]string, error) {
	cmd := exec.Command("tinygo", "list", "-target="+root.Target, "-deps", ".")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		return nil, fmt.Errorf("listing dependencies: %w\n%s", err, strings.TrimSpace(string(stderr)))
	}
	var fws []string
	for _, line := range strings.Split(string(out), "\n") {
		pkg, ok := strings.CutPrefix(strings.TrimSpace(line), assets.LibModule+"/")
		if !ok {
			continue
		}
		if st, err := os.Stat(filepath.Join(root.Frameworks, pkg)); err == nil && st.IsDir() {
			fws = append(fws, pkg)
		}
	}
	sort.Strings(fws)
	return fws, nil
}

// keptSymbols stay global in the Go object: the Go entry points the C side
// calls. Everything else is made local.
var keptSymbols = []string{
	"gopgext_go_*",
	"tinygo_pgext_*",
	"tinygo_scanstack",
}

// CSrcDir is the project subdirectory whose .c/.h files are compiled into the
// library (Go rejects .c files in the package directory without cgo).
const CSrcDir = "csrc"

// SQLDir is the project subdirectory whose .sql files are appended (in name
// order) to the generated install script.
const SQLDir = "sql"

// GeneratedGoFile holds the generated Go wrappers in the build copy.
const GeneratedGoFile = "zz_gopgext_generated.go"

func (b *builder) writeExt(root *assets.Root, proj, ext, name, version string, heapSize, heapMax uint64,
	pg *PGConfig, funcs []*Function) error {
	objs := []string{"go.o"}
	var cExports []string // V1 functions of the project's C sources
	// The C runtime.
	entries, err := os.ReadDir(root.CSrc)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if err := copyFile(filepath.Join(root.CSrc, n), filepath.Join(ext, n), 0o644); err != nil {
			return err
		}
		if strings.HasSuffix(n, ".c") {
			objs = append(objs, strings.TrimSuffix(n, ".c")+".o")
		}
	}
	if err := os.WriteFile(filepath.Join(ext, "functions.c"), append(genC(funcs), genAliases(b.aliases)...), 0o644); err != nil {
		return err
	}
	for _, a := range b.aliases {
		cExports = append(cExports, a.Name)
	}
	objs = append(objs, "functions.o")

	// The project's own C sources.
	if entries, err := os.ReadDir(filepath.Join(proj, CSrcDir)); err == nil {
		var user []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || (!strings.HasSuffix(n, ".c") && !strings.HasSuffix(n, ".h")) {
				continue
			}
			// Into ext/csrc/ with their names, so they can include each other.
			if err := os.MkdirAll(filepath.Join(ext, CSrcDir), 0o755); err != nil {
				return err
			}
			if err := copyFile(filepath.Join(proj, CSrcDir, n), filepath.Join(ext, CSrcDir, n), 0o644); err != nil {
				return err
			}
			if strings.HasSuffix(n, ".c") {
				user = append(user, CSrcDir+"/"+strings.TrimSuffix(n, ".c")+".o")
				src, err := os.ReadFile(filepath.Join(proj, CSrcDir, n))
				if err != nil {
					return err
				}
				for _, m := range v1Re.FindAllSubmatch(src, -1) {
					cExports = append(cExports, string(m[1]))
				}
			}
		}
		if len(user) > 0 {
			b.step("adding C sources from %s/", CSrcDir)
			objs = append(objs, user...)
		}
	}
	// Framework glue: frameworks/<pkg>/ of the imported gopgext packages.
	for _, fw := range b.frameworks {
		dir := filepath.Join(root.Frameworks, fw)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sub := "fw_" + fw
		if err := os.MkdirAll(filepath.Join(ext, sub), 0o755); err != nil {
			return err
		}
		for _, e := range entries {
			n := e.Name()
			data, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				return err
			}
			switch {
			case n == "ldflags":
				b.ldflags = append(b.ldflags, strings.Fields(string(data))...)
			case n == "install.sql":
				b.fwSQL = append(b.fwSQL, []byte(fmt.Sprintf("\n-- gopgext/%s\n%s", fw,
					strings.ReplaceAll(string(data), "@EXTENSION@", name))))
			case strings.HasSuffix(n, ".c") || strings.HasSuffix(n, ".h"):
				if err := os.WriteFile(filepath.Join(ext, sub, n), data, 0o644); err != nil {
					return err
				}
				if strings.HasSuffix(n, ".c") {
					objs = append(objs, sub+"/"+strings.TrimSuffix(n, ".c")+".o")
					for _, m := range v1Re.FindAllSubmatch(data, -1) {
						cExports = append(cExports, string(m[1]))
					}
				}
			}
		}
	}
	if len(b.frameworks) > 0 {
		b.step("using frameworks: gopgext/%s", strings.Join(b.frameworks, ", gopgext/"))
	}
	sort.Strings(objs[1:])

	info := fmt.Sprintf(`/* Generated by gopgext. */
#define GOPGEXT_NAME %s
#define GOPGEXT_VERSION %s
#define GOPGEXT_HEAP_SIZE %dUL
#define GOPGEXT_HEAP_MAX %dUL
`, cString(name), cString(version), heapSize, heapMax)
	if err := os.WriteFile(filepath.Join(ext, "modinfo.h"), []byte(info), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ext, "exports.map"), genExports(funcs, cExports), 0o644); err != nil {
		return err
	}
	// -Bsymbolic: calls between the library's exported functions (an alias
	// calling a framework's V1 function) stay in the library, even when
	// another gopgext library loaded earlier exports the same name.
	makefile := fmt.Sprintf(`# Generated by gopgext. go.o is prebuilt by TinyGo.
MODULE_big = %s
OBJS = %s
PG_CPPFLAGS = -I. -I%s
SHLIB_LINK = -Wl,--version-script=exports.map -Wl,-Bsymbolic -Wl,-z,noexecstack %s
PG_CONFIG = %s
PGXS := $(shell $(PG_CONFIG) --pgxs)
include $(PGXS)
`, name, strings.Join(objs, " "), CSrcDir, strings.Join(b.ldflags, " "), pg.Path)
	return os.WriteFile(filepath.Join(ext, "Makefile"), []byte(makefile), 0o644)
}

// exec runs cmd. Output is shown only in verbose mode or when it fails.
func (b *builder) exec(cmd *exec.Cmd) error {
	if b.opts.Verbose {
		fmt.Fprintf(b.opts.Log, "+ %s\n", strings.Join(cmd.Args, " "))
		cmd.Stdout = b.opts.Log
		cmd.Stderr = b.opts.Log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", filepath.Base(cmd.Path), err)
		}
		return nil
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w\n%s", filepath.Base(cmd.Path), err, strings.TrimSpace(buf.String()))
	}
	return nil
}

// PGConfig describes the PostgreSQL installation to build for.
type PGConfig struct {
	Path       string // pg_config executable
	Version    string // e.g. "19.0"
	BinDir     string
	PkgLibDir  string // where libraries are installed
	ExtDir     string // where control and SQL files are installed
	TSearchDir string // where text search data files are installed
}

// FindPGConfig locates pg_config (path, else $PG_CONFIG, else PATH) and
// queries it.
func FindPGConfig(path string) (*PGConfig, error) {
	if path == "" {
		path = os.Getenv("PG_CONFIG")
	}
	if path == "" {
		path = "pg_config"
	}
	full, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("pg_config not found (%v): install PostgreSQL's server development files, "+
			"or use -pg-config or $PG_CONFIG", err)
	}
	full, _ = filepath.Abs(full)
	out, err := exec.Command(full, "--version", "--bindir", "--pkglibdir", "--sharedir", "--includedir-server").Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", full, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 5 {
		return nil, fmt.Errorf("%s: unexpected output %q", full, out)
	}
	pg := &PGConfig{
		Path:       full,
		Version:    strings.TrimPrefix(strings.TrimSpace(lines[0]), "PostgreSQL "),
		BinDir:     strings.TrimSpace(lines[1]),
		PkgLibDir:  strings.TrimSpace(lines[2]),
		ExtDir:     filepath.Join(strings.TrimSpace(lines[3]), "extension"),
		TSearchDir: filepath.Join(strings.TrimSpace(lines[3]), "tsearch_data"),
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(lines[4]), "postgres.h")); err != nil {
		return nil, fmt.Errorf("PostgreSQL %s server headers not found in %s: install its server development package",
			pg.Version, strings.TrimSpace(lines[4]))
	}
	major, _ := strconv.Atoi(strings.SplitN(pg.Version, ".", 2)[0])
	if major < 14 {
		return nil, fmt.Errorf("PostgreSQL %s is not supported (14 or later required)", pg.Version)
	}
	return pg, nil
}

// GoMod is the subset of go.mod gopgext cares about.
type GoMod struct {
	Path      string
	GoVersion string
}

var (
	moduleRe  = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)
	goVerRe   = regexp.MustCompile(`(?m)^go\s+(\S+)`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
)

// ReadGoMod parses the module path and go version from a go.mod file.
func ReadGoMod(path string) (*GoMod, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found (run `gopgext init` first)", path)
		}
		return nil, err
	}
	m := moduleRe.FindSubmatch(data)
	if m == nil {
		return nil, fmt.Errorf("%s: no module directive", path)
	}
	gm := &GoMod{Path: string(m[1]), GoVersion: assets.LibGoVersion}
	if v := goVerRe.FindSubmatch(data); v != nil {
		gm.GoVersion = string(v[1])
	}
	return gm, nil
}

// NameFromPath derives an extension name from a Go module path: last path
// element, lower-cased, with characters other than letters, digits and _
// replaced by "_".
func NameFromPath(p string) string {
	base := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
	var sb strings.Builder
	for _, r := range base {
		if r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

var validName = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// ValidateName checks name can be used as an extension (and library) name.
func ValidateName(name string) error {
	if !validName.MatchString(name) || len(name) > 63 {
		return fmt.Errorf("invalid extension name %q: use lower-case letters, digits, _ and - (max 63 chars)", name)
	}
	switch name {
	case "go", "runtime", "spi", "functions":
		return fmt.Errorf("extension name %q is reserved, pick another with -name", name)
	}
	return nil
}

// CacheDir is where gopgext keeps extracted assets and build directories.
func CacheDir() (string, error) {
	if d := os.Getenv("GOPGEXTCACHE"); d != "" {
		return d, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "gopgext"), nil
}

// PrepareAssets materializes the assets for the installed TinyGo.
func PrepareAssets() (*assets.Root, error) {
	cache, err := CacheDir()
	if err != nil {
		return nil, err
	}
	tgRoot, tgVersion, err := tinygoInfo()
	if err != nil {
		return nil, err
	}
	return assets.Prepare(cache, tgRoot, tgVersion)
}

func tinygoInfo() (root, version string, err error) {
	if _, err := exec.LookPath("tinygo"); err != nil {
		return "", "", errors.New("tinygo not found in PATH (https://tinygo.org/getting-started/install/)")
	}
	out, err := exec.Command("tinygo", "env", "TINYGOROOT").Output()
	if err != nil {
		return "", "", fmt.Errorf("tinygo env TINYGOROOT: %w", err)
	}
	root = strings.TrimSpace(string(out))
	if os.Getenv("TINYGOROOT") != "" {
		// Don't stack our overlay on top of a user-provided overlay by accident.
		root = os.Getenv("TINYGOROOT")
	}
	out, err = exec.Command("tinygo", "version").Output()
	if err != nil {
		return "", "", fmt.Errorf("tinygo version: %w", err)
	}
	return root, strings.TrimSpace(string(out)), nil
}

// workMarker identifies go.work files written by gopgext.
const workMarker = "// Generated by gopgext"

// GoWorkContent returns a go.work using the project and the gopgext lib module.
func GoWorkContent(goVersion, proj, lib string, forEditor bool) string {
	var sb strings.Builder
	sb.WriteString(workMarker)
	if forEditor {
		sb.WriteString(" so editors can resolve \"gopgext/pg\". `gopgext build`\n// rewrites it when needed; it's machine-specific, don't commit it.")
	}
	fmt.Fprintf(&sb, "\n\ngo %s\n\nuse (\n\t%s\n\t%s\n)\n", goVersion, proj, lib)
	return sb.String()
}

// RefreshProjectGoWork updates the project's editor go.work if gopgext
// created it and it points to an outdated asset directory.
func RefreshProjectGoWork(proj, goVersion, lib string) {
	p := filepath.Join(proj, "go.work")
	data, err := os.ReadFile(p)
	if err != nil || !bytes.HasPrefix(data, []byte(workMarker)) {
		return
	}
	if want := GoWorkContent(goVersion, ".", lib, true); string(data) != want {
		os.WriteFile(p, []byte(want), 0o644)
	}
}

// shortTinyGoVersion turns "tinygo version 0.43.0 linux/amd64 (...)" into "0.43.0".
func shortTinyGoVersion(v string) string {
	if f := strings.Fields(v); len(f) >= 3 && f[0] == "tinygo" {
		return f[2]
	}
	return v
}

// parseSize parses sizes like 8192, 8K, 4M, 1G.
func parseSize(s string) uint64 {
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return (n * mult) &^ 4095
}

func cString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
