// Command gopgext builds PostgreSQL extensions from Go code using TinyGo.
//
//	gopgext init [dir]       create a new extension project
//	gopgext build [dir]      compile it into <name>.so, <name>.control, <name>--<version>.sql
//	gopgext install [dir]    build it and install it into PostgreSQL
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jespino/gopgext/internal/build"
)

const version = "0.1.0"

func usage() {
	fmt.Fprint(os.Stderr, `gopgext - build PostgreSQL extensions from Go code (with TinyGo)

Usage:
  gopgext init [-module path] [dir]   create a new extension project in dir (default .)
  gopgext build [flags] [dir]         compile the project in dir (default .)
  gopgext install [flags] [dir]       build and install into PostgreSQL's directories
  gopgext version                     print the gopgext version

Run "gopgext <command> -h" for the command's flags.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "build":
		err = cmdBuild(os.Args[2:], false)
	case "install":
		err = cmdBuild(os.Args[2:], true)
	case "version", "-version", "--version":
		fmt.Println("gopgext", version)
	case "help", "-h", "-help", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "gopgext: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "gopgext:", err)
		}
		os.Exit(1)
	}
}

func cmdBuild(args []string, install bool) error {
	cmd := "build"
	if install {
		cmd = "install"
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var o build.Options
	fs.StringVar(&o.OutDir, "o", "", "output `dir` for the .so, .control and .sql files (default: the project dir)")
	fs.StringVar(&o.Name, "name", "", "extension `name` (default: last element of the go.mod module path)")
	fs.StringVar(&o.Version, "version", build.DefaultVersion, "extension `version`")
	fs.StringVar(&o.Comment, "comment", "", "control file comment `text`")
	fs.BoolVar(&o.Trusted, "trusted", false, "mark the extension trusted (non-superusers with CREATE privilege can install it)")
	fs.Func("requires", "comma-separated `extensions` this one depends on (control file requires)", func(v string) error {
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				o.Requires = append(o.Requires, r)
			}
		}
		return nil
	})
	fs.StringVar(&o.PGConfig, "pg-config", "", "`pg_config` of the PostgreSQL to build for (default: $PG_CONFIG, else pg_config in PATH)")
	fs.StringVar(&o.HeapSize, "heap-size", build.DefaultHeapSize, "initial `size` of each backend's garbage-collected Go heap, e.g. 512K, 4M")
	fs.StringVar(&o.HeapMax, "heap-max", build.DefaultHeapMax, "`size` limit the Go heap can grow to (address space reserved per backend)")
	fs.BoolVar(&o.Verbose, "v", false, "print the commands run and their output")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: gopgext %s [flags] [dir]\n", cmd)
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("%s takes at most one directory", cmd)
	}
	o.Dir = "."
	if fs.NArg() == 1 {
		o.Dir = fs.Arg(0)
	}
	res, err := build.Build(o)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "gopgext: built extension %q %s for PostgreSQL %s: %s\n",
		res.Name, res.Version, res.PG.Version, strings.Join(rel(res.Library, res.Control, res.Script), ", "))
	if o.Verbose {
		fmt.Fprintf(os.Stderr, "gopgext: work dir %s\n", res.WorkDir)
	}
	if !install {
		return nil
	}
	for _, f := range []struct{ src, dir string }{
		{res.Library, res.PG.PkgLibDir},
		{res.Control, res.PG.ExtDir},
		{res.Script, res.PG.ExtDir},
	} {
		if err := installFile(f.src, f.dir); err != nil {
			return err
		}
	}
	for _, f := range res.DataFiles {
		if err := installFile(f, res.PG.TSearchDir); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "gopgext: installed into %s and %s\n", res.PG.PkgLibDir, res.PG.ExtDir)
	if len(res.DataFiles) > 0 {
		fmt.Fprintf(os.Stderr, "gopgext: installed %d text search data files into %s\n", len(res.DataFiles), res.PG.TSearchDir)
	}
	ident := res.Name
	if strings.Contains(ident, "-") {
		ident = `"` + ident + `"`
	}
	fmt.Fprintf(os.Stderr, "  load it with: CREATE EXTENSION %s;\n", ident)
	fmt.Fprintf(os.Stderr, "  (sessions that already loaded an older %s.so keep using it: reconnect)\n", res.Name)
	return nil
}

// installFile copies src into dir, through a temporary file and a rename so
// backends that have the old library mapped are unaffected.
func installFile(src, dir string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(src, ".so") {
		mode = 0o755
	}
	dst := filepath.Join(dir, filepath.Base(src))
	tmp, err := os.CreateTemp(dir, ".gopgext-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("no permission to write to %s (run install with sudo, or install the files by hand)", dir)
		}
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func rel(paths ...string) []string {
	wd, _ := os.Getwd()
	var out []string
	for _, p := range paths {
		if r, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(r, "..") {
			p = r
		}
		out = append(out, p)
	}
	return out
}

var mainTmpl = template.Must(template.New("main").Parse(`// {{.Name}} is a PostgreSQL extension written in Go, built with gopgext.
//
// Functions marked with //gopgext:function become SQL functions. main runs
// once per backend, when PostgreSQL loads the extension's library.
//
//	gopgext install && psql -c 'CREATE EXTENSION {{.Name}}' -c "SELECT hello('world')"
package main

import (
	"strings"

	"gopgext/pg"
)

// Hello returns a greeting for name.
//
//gopgext:function immutable parallel=safe
func Hello(name string) string {
	return "Hello, " + name + "!"
}

// Shout upper-cases s. An empty string raises an error.
//
//gopgext:function immutable parallel=safe
func Shout(s string) (string, error) {
	if s == "" {
		return "", pg.Errorf(pg.CodeInvalidParameterValue, "nothing to shout")
	}
	return strings.ToUpper(s) + "!", nil
}

func main() {}
`))

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	modPath := fs.String("module", "", "Go module `path` (default: the directory name)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: gopgext init [-module path] [dir]")
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("init takes at most one directory")
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if *modPath == "" {
		*modPath = filepath.Base(abs)
	}
	name := build.NameFromPath(*modPath)
	if err := build.ValidateName(name); err != nil {
		return fmt.Errorf("%w (derived from module path %q; use -module)", err, *modPath)
	}
	for _, f := range []string{"go.mod", "main.go"} {
		if _, err := os.Stat(filepath.Join(abs, f)); err == nil {
			return fmt.Errorf("%s already exists in %s", f, abs)
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	goVersion := "1.23"
	goMod := fmt.Sprintf("module %s\n\ngo %s\n", *modPath, goVersion)
	if err := os.WriteFile(filepath.Join(abs, "go.mod"), []byte(goMod), 0o644); err != nil {
		return err
	}
	var main strings.Builder
	if err := mainTmpl.Execute(&main, map[string]string{"Name": name}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(abs, "main.go"), []byte(main.String()), 0o644); err != nil {
		return err
	}

	// Editor support: a go.work that makes "gopgext/pg" resolvable. Optional,
	// so failures here (e.g. TinyGo not installed yet) are only warnings.
	if root, err := build.PrepareAssets(); err != nil {
		fmt.Fprintf(os.Stderr, "gopgext: warning: skipping go.work for editor support: %v\n", err)
	} else {
		work := build.GoWorkContent(goVersion, ".", root.Lib, true)
		if err := os.WriteFile(filepath.Join(abs, "go.work"), []byte(work), 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "gopgext: created extension %q in %s\n", name, abs)
	fmt.Fprintf(os.Stderr, "  build:    gopgext build %s\n", dir)
	fmt.Fprintf(os.Stderr, "  install:  gopgext install %s   (may need sudo)\n", dir)
	fmt.Fprintf(os.Stderr, "  use:      CREATE EXTENSION %s; SELECT hello('world');\n", name)
	return nil
}

// parseInterspersed parses flags that may appear before or after positional
// arguments ("gopgext build dir -v").
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return fs.Parse(pos)
}
