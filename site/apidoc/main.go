// Command apidoc writes Hugo pages with the API reference of the Go packages
// in a directory, from their doc comments (like pkg.go.dev, for packages that
// pkg.go.dev can't fetch: they only build with TinyGo and a custom target).
//
//	go run . -src ../../internal/assets/_files/lib -import gopgext -out ../content/api
//
// Each package becomes <out>/<name>.md; <out>/_index.md lists them.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	src := flag.String("src", "", "directory whose subdirectories are the packages")
	importPrefix := flag.String("import", "", "import path of -src (e.g. gopgext)")
	out := flag.String("out", "", "output directory (replaced)")
	skip := flag.String("skip", "internal", "comma-separated packages to leave out")
	title := flag.String("title", "API reference", "title of the index page")
	flag.Parse()
	if *src == "" || *importPrefix == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	skipped := map[string]bool{}
	for s := range strings.SplitSeq(*skip, ",") {
		skipped[strings.TrimSpace(s)] = true
	}

	entries, err := os.ReadDir(*src)
	if err != nil {
		log.Fatal(err)
	}
	var pkgs []*pkgDoc
	for _, e := range entries {
		if !e.IsDir() || skipped[e.Name()] {
			continue
		}
		p, err := load(filepath.Join(*src, e.Name()), *importPrefix+"/"+e.Name())
		if err != nil {
			log.Fatal(err)
		}
		if p != nil {
			pkgs = append(pkgs, p)
		}
	}
	if err := os.RemoveAll(*out); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	var index bytes.Buffer
	fmt.Fprintf(&index, "---\ntitle: %q\ndescription: \"The Go packages extensions import, generated from their doc comments.\"\nindexTitle: \"All packages\"\ncards: false\ngenerated: true\nweight: 2\n---\n\n", *title)
	fmt.Fprintf(&index, "Generated from the packages' doc comments. Import them from your project's Go code.\n\n")
	fmt.Fprintf(&index, "| Package | Synopsis |\n|---|---|\n")
	for i, p := range pkgs {
		fmt.Fprintf(&index, "| [%s](%s/) | %s |\n", p.importPath, p.doc.Name, cell(p.doc.Synopsis(p.doc.Doc)))
		page := p.render(i + 1)
		if err := os.WriteFile(filepath.Join(*out, p.doc.Name+".md"), page, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(*out, "_index.md"), index.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("apidoc: %d packages into %s\n", len(pkgs), *out)
}

type pkgDoc struct {
	importPath string
	fset       *token.FileSet
	comments   []*ast.CommentGroup
	doc        *doc.Package
}

func load(dir, importPath string) (*pkgDoc, error) {
	fset := token.NewFileSet()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var files []*ast.File
	var comments []*ast.CommentGroup
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
		comments = append(comments, f.Comments...)
	}
	if len(files) == 0 {
		return nil, nil
	}
	d, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		return nil, err
	}
	return &pkgDoc{importPath: importPath, fset: fset, comments: comments, doc: d}, nil
}

func (p *pkgDoc) render(weight int) []byte {
	var b bytes.Buffer
	d := p.doc
	fmt.Fprintf(&b, "---\ntitle: %q\nlinkTitle: %q\ndescription: %q\nweight: %d\nhideDescription: true\ngenerated: true\n---\n\n", p.importPath, d.Name, oneLine(d.Synopsis(d.Doc)), weight)
	fmt.Fprintf(&b, "```go\nimport \"%s\"\n```\n\n", p.importPath)
	b.WriteString(p.markdown(d.Doc))

	// Index.
	b.WriteString("\n## Index {#pkg-index}\n\n")
	if len(d.Consts) > 0 {
		b.WriteString("- [Constants](#pkg-constants)\n")
	}
	if len(d.Vars) > 0 {
		b.WriteString("- [Variables](#pkg-variables)\n")
	}
	for _, f := range d.Funcs {
		fmt.Fprintf(&b, "- [%s](#%s)\n", code(p.signature(f.Decl)), f.Name)
	}
	for _, t := range d.Types {
		fmt.Fprintf(&b, "- [type %s](#%s)\n", t.Name, t.Name)
		for _, f := range t.Funcs {
			fmt.Fprintf(&b, "  - [%s](#%s)\n", code(p.signature(f.Decl)), f.Name)
		}
		for _, m := range t.Methods {
			fmt.Fprintf(&b, "  - [%s](#%s.%s)\n", code(p.signature(m.Decl)), t.Name, m.Name)
		}
	}

	if len(d.Consts) > 0 {
		b.WriteString("\n## Constants {#pkg-constants}\n\n")
		for _, v := range d.Consts {
			p.value(&b, v)
		}
	}
	if len(d.Vars) > 0 {
		b.WriteString("\n## Variables {#pkg-variables}\n\n")
		for _, v := range d.Vars {
			p.value(&b, v)
		}
	}
	if len(d.Funcs) > 0 {
		b.WriteString("\n## Functions {#pkg-functions}\n")
		for _, f := range d.Funcs {
			p.fn(&b, f, f.Name, "###")
		}
	}
	if len(d.Types) > 0 {
		b.WriteString("\n## Types {#pkg-types}\n")
		for _, t := range d.Types {
			fmt.Fprintf(&b, "\n### type %s {#%s}\n\n", t.Name, t.Name)
			b.WriteString(p.decl(t.Decl))
			b.WriteString(p.markdown(t.Doc))
			for _, v := range t.Consts {
				p.value(&b, v)
			}
			for _, v := range t.Vars {
				p.value(&b, v)
			}
			for _, f := range t.Funcs {
				p.fn(&b, f, f.Name, "####")
			}
			for _, m := range t.Methods {
				p.fn(&b, m, t.Name+"."+m.Name, "####")
			}
		}
	}
	return b.Bytes()
}

func (p *pkgDoc) fn(b *bytes.Buffer, f *doc.Func, id, level string) {
	name := f.Name
	if f.Recv != "" {
		name = "(" + f.Recv + ") " + f.Name
	}
	fmt.Fprintf(b, "\n%s func %s {#%s}\n\n", level, name, id)
	b.WriteString(p.decl(f.Decl))
	b.WriteString(p.markdown(f.Doc))
}

func (p *pkgDoc) value(b *bytes.Buffer, v *doc.Value) {
	b.WriteString("\n")
	b.WriteString(p.decl(v.Decl))
	b.WriteString(p.markdown(v.Doc))
}

// decl prints a declaration (with the comments inside it, e.g. on struct
// fields) as a Go code block.
func (p *pkgDoc) decl(node ast.Node) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, p.fset, &printer.CommentedNode{Node: node, Comments: p.comments}); err != nil {
		log.Fatal(err)
	}
	return "```go\n" + buf.String() + "\n```\n\n"
}

// signature is a function's declaration on one line, for the index.
func (p *pkgDoc) signature(fd *ast.FuncDecl) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), &ast.FuncDecl{Recv: fd.Recv, Name: fd.Name, Type: fd.Type}); err != nil {
		log.Fatal(err)
	}
	return oneLine(buf.String())
}

func (p *pkgDoc) markdown(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	pr := p.doc.Printer()
	pr.HeadingLevel = 4
	pr.DocLinkURL = func(l *comment.DocLink) string {
		id := l.Name
		if l.Recv != "" {
			id = l.Recv + "." + l.Name
		}
		switch {
		case l.ImportPath == "" || l.ImportPath == p.importPath:
			return "#" + id
		case strings.Contains(l.ImportPath, "/") && strings.HasPrefix(l.ImportPath, strings.SplitN(p.importPath, "/", 2)[0]+"/"):
			u := "../" + l.ImportPath[strings.LastIndex(l.ImportPath, "/")+1:] + "/"
			if id != "" {
				u += "#" + id
			}
			return u
		default:
			u := "https://pkg.go.dev/" + l.ImportPath
			if id != "" {
				u += "#" + id
			}
			return u
		}
	}
	return fenceCode(string(pr.Markdown(p.doc.Parser().Parse(text)))) + "\n"
}

// fenceCode turns the tab-indented code blocks of the Markdown printer into
// fenced blocks, Go or SQL, so they get syntax highlighting.
func fenceCode(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "\t") {
			out = append(out, lines[i])
			i++
			continue
		}
		var block []string
		for i < len(lines) && (strings.HasPrefix(lines[i], "\t") || lines[i] == "" && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t")) {
			block = append(block, strings.TrimPrefix(lines[i], "\t"))
			i++
		}
		lang := "go"
		body := strings.Join(block, "\n")
		for _, kw := range []string{"CREATE ", "SELECT ", "INSERT ", "ALTER ", "DROP ", "UPDATE ", "DELETE ", "EXPLAIN "} {
			if strings.Contains(body, kw) && !strings.Contains(body, "func ") {
				lang = "sql"
			}
		}
		out = append(out, "```"+lang)
		out = append(out, block...)
		out = append(out, "```")
	}
	return strings.Join(out, "\n")
}

// cell escapes text for a Markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "|", "\\|", "*", "\\*", "_", "\\_").Replace(oneLine(s))
}

func code(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
