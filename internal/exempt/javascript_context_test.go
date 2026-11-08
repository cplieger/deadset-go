package exempt

import (
	"html/template"
	"path/filepath"
	"strings"
	"testing"
)

// templateFile is one file of an html/template set: its path and its text.
type templateFile struct{ path, src string }

// readByName reports whether the analysis reads a set of files, each added as a
// ParseFiles call adds it, or as a Parse call adds a text when guessed, by the
// names its actions name.
func readByName(files []templateFile, guessed bool) bool {
	inv := &templateSet{names: make(map[string]bool), complete: true}
	for _, f := range files {
		inv.read(htmlTemplate, f.path, filepath.Base(f.path), []byte(f.src), guessed)
	}
	return inv.complete && !encodesWhole(inv.trees, inv.guessed)
}

// encodesSecret reports whether html/template, executing the first file of a set
// with a value whose Secret field no action names, writes Secret's JSON key or
// Secret's value as a JavaScript string escaper writes it.
func encodesSecret(t *testing.T, files []templateFile) bool {
	t.Helper()
	set := template.New(files[0].path).Funcs(template.FuncMap{"unknown": func(...any) string { return "" }})
	for i, f := range files {
		one := set
		if i > 0 {
			one = set.New(f.path)
		}
		if _, err := one.Parse(f.src); err != nil {
			t.Fatalf("Setup: parse %s: %v", f.path, err)
		}
	}
	var out strings.Builder
	_ = set.Execute(&out, struct{ Name, Secret string }{"n", "s'x"})
	return strings.Contains(out.String(), "Secret&#34;:") || strings.Contains(out.String(), `"Secret":`) ||
		strings.Contains(out.String(), `s\u0027x`)
}

func TestEncodesWholeAnswersAsHTMLTemplateEscapes(t *testing.T) {
	t.Parallel()
	one := func(src string) []templateFile { return []templateFile{{"p.html", src}} }
	cases := []struct {
		name    string
		files   []templateFile
		guessed bool
		want    bool
	}{
		{"script element", one(`<p>{{.Name}}</p><SCRIPT>var a = {{.}}</SCRIPT>`), false, true},
		{"valued handler", one(`<button onclick="go({{.}})">x</button>`), false, true},
		{"valueless handler", one(`<button onclick>{{.Name}}</button>`), false, false},
		{"handler after a quoted tag end", one(`<a title="a>b" onclick=go({{.}})>{{.Name}}</a>`), false, true},
		{"handler holding no action", one(`<a title="a>b" onclick="go()">{{.Name}}</a>`), false, false},
		{"handler in upper case", one(`<a ONCLICK='go({{.}})'>{{.Name}}</a>`), false, true},
		{"handler after a self-closing slash", one(`<img/onerror=go({{.}})>`), false, false},
		{"handler behind a data- prefix", one(`<div data-onclick="f({{.}})">x</div>`), false, true},
		{"handler behind a namespace prefix", one(`<svg:a xlink:onclick="f({{.}})">x</svg:a>`), false, true},
		{"handler after an action in the tag", one(`<a {{if true}}onclick="f({{.}})"{{end}}>x</a>`), false, true},
		{"tag end inside an action", one(`<p {{/* > */}} onclick="f({{.}})">x</p>`), false, true},
		{"handler name after the xmlns prefix", one(`<svg xmlns:onload="{{.}}">{{.Name}}</svg>`), false, false},
		{"handler named in an action's string", one(`<p {{"onclick=f()"}}>{{.Name}}</p>`), false, false},
		{"handler in a defined template", one(`{{define "h"}}<a onclick="f({{.}})">x</a>{{end}}{{.Name}}`), false, true},
		{"plain markup", one(`<p class="on">{{.Name}}</p><a href="/on">x</a>`), false, false},
		{"handler named in a quoted value", one(`<a title="onclick=go({{.}})">x</a>`), false, false},
		{"handler in a comment", one(`<!-- <a onclick=go({{.}})> --><p>{{.Name}}</p>`), false, false},
		{"handler in a raw text element", one(`<textarea><a onclick=go({{.}})></TEXTAREA><p>{{.Name}}</p>`), false, false},
		{"handler after a raw text element", one(`<title>t</title><a onclick=go({{.}})>`), false, true},
		{"script holding no action", one(`<script>var a = 1</script>{{.Name}}`), false, false},
		{"JSON script type", one(`<script type="application/json">{{.}}</script>`), false, true},
		{"script type that is not JavaScript", one(`<script type="text/template">{{.}}</script>`), false, false},
		{"value in a JavaScript string", one(`<script>var a = "{{.}}"</script>`), false, true},
		{"value in a handler's string", one(`<button onclick="f('{{.}}')">x</button>`), false, true},
		{"value in a template literal", one("<script>var a = `{{.}}`</script>"), false, true},
		{"value in a regular expression", one(`<script>var r = /{{.}}/</script>`), false, true},
		{"variable holding the value in a JavaScript string", one(`{{$v := .}}<script>var a = "{{$v}}"</script>`), false, true},
		{"start tag left open", one(`<title`), false, true},
		{"start tag left open by its quoted value", one(`<a title="x`), false, true},
		{"handler written by a template called in the tag", []templateFile{
			{"page.html", `<a {{template "attrs" .}}>{{.Name}}</a>`},
			{"attrs.html", `{{define "attrs"}}onclick="f({{.}})"{{end}}`},
		}, false, true},
		{"template called in text", []templateFile{
			{"page.html", `<p>{{template "t" .}}</p>`},
			{"t.html", `{{define "t"}}{{.Name}}{{end}}`},
		}, false, false},
		{"start tag a called template leaves open", []templateFile{
			{"card.html", `{{template "open" .}}onclick="f({{.}})">{{.Name}}</b>`},
			{"open.html", `{{define "open"}}<b {{end}}`},
		}, false, true},
		{"template called after text of an unquoted value", one(`{{define "value"}}{{.Name}}{{end}}<a title=x{{template "value" .}}>x</a>`), false, false},
		{"template called in a quoted value", one(`{{define "value"}}{{.Name}}{{end}}<a title="{{template "value" .}}">x</a>`), false, false},
		{"template called as an unquoted value", one(`{{define "value"}}{{.Name}}{{end}}<a title={{template "value" .}}>x</a>`), false, true},
		{"called template closing a quoted value with a handler", one(`{{define "v"}}x" onclick="f({{.}}){{end}}<a title="{{template "v" .}}">x</a>`), false, true},
		{"called template closing an unquoted value with a handler", one(`{{define "v"}}x onclick=f({{.}}){{end}}<a class={{template "v" .}}>x</a>`), false, true},
		{"called template of another file closing a value", []templateFile{
			{"page.html", `<a title="{{template "v" .}}">x</a>`},
			{"v.html", `{{define "v"}}x" onclick="f({{.}}){{end}}`},
		}, false, true},
		{"template called in a script element", []templateFile{
			{"page.html", `<script>{{template "data" .}}</script>{{.Name}}`},
			{"data.html", `{{define "data"}}var a = {{.}};{{end}}`},
		}, false, true},
		{"template called in a JavaScript string", one(`{{define "s"}}{{.}}{{end}}<script>var a = "{{template "s" .}}";</script>`), false, true},
		{"script template called twice, ending in another context", []templateFile{
			{"page.html", `<script>{{template "t" .}}/{{.}}/</script>`},
			{"a.html", `<script>{{template "t" .}}</script>`},
			{"t.html", `{{define "t"}}x{{end}}`},
		}, false, true},
		{"URL template called twice, ending in another URL part", []templateFile{
			{"page.html", `<a href="{{template "t" .}}{{.Name}}">x</a>`},
			{"a.html", `<a href="{{template "t" .}}">home</a>`},
			{"t.html", `{{define "t"}}/users/{{end}}`},
		}, false, true},
		{"attribute name template called twice", []templateFile{
			{"page.html", `<a {{template "t" .}}onclick="f({{.}})">{{.Name}}</a>`},
			{"a.html", `<a {{template "t" .}}>x</a>`},
			{"t.html", `{{define "t"}}data-x{{end}}`},
		}, false, true},
		{"template called by a template called in a handler", one(`{{define "w"}}g({{template "v" .}}){{end}}{{define "v"}}{{.}}{{end}}<a onclick="{{template "w" .}}">x</a>`), false, true},
		{"recursive template called in a value", one(`{{define "r"}}{{if .Name}}{{.Name}}{{else}}{{template "r" .}}{{end}}{{end}}<a title="{{template "r" .}}">x</a>`), false, false},
		{"template left open called from two files", []templateFile{
			{"a.html", `<p>{{template "open" .}}x>`},
			{"b.html", `{{template "open" .}}onclick="f({{.}})">`},
			{"open.html", `{{define "open"}}<b {{end}}`},
		}, false, true},
		{"guessed name called in a value", []templateFile{
			{"page.html", `<a title="{{template "t" .}}">x</a>`},
			{"t.html", `{{define "t"}}{{.Name}}{{end}}`},
		}, true, true},
		{"guessed name called in text", []templateFile{
			{"page.html", `<p>{{template "t.html" .}}</p>`},
			{"t.html", `{{.Name}}`},
		}, true, false},
		{"unknown function", one(`<p>{{unknown .Name}}</p><script>var a = {{unknown .}}</script>`), false, true},
		{"unknown function in text", one(`<p>{{unknown .Name}}</p>`), false, false},
		{"template the set does not define", one(`<p>{{template "nowhere" .}}</p>`), false, true},
		{"predefined escaper before another command", one(`<a title={{. | html | len}}>x</a>`), false, true},
		{"one name defined with two texts, the later one used", []templateFile{
			{"a.html", `{{template "x" .}}{{define "x"}}<script>var a = {{.}}</script>{{end}}`},
			{"b.html", `{{define "x"}}<b>{{.Name}}</b>{{end}}`},
		}, false, false},
		{"block overridden by a later file", []templateFile{
			{"base.html", `<main>{{block "content" .}}<p>nothing</p>{{end}}</main>`},
			{"home.html", `{{define "content"}}<h1>{{.Name}}</h1>{{end}}`},
		}, false, false},
		{"one file read twice", []templateFile{
			{"a/x.html", `{{.Name}}`},
			{"b/x.html", `{{.Name}}`},
		}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := !readByName(c.files, c.guessed); got != c.want {
				t.Errorf("not read by name = %v, want %v, for %q", got, c.want, c.files)
			}
			for range 50 {
				if again := !readByName(c.files, c.guessed); again != c.want {
					t.Fatalf("not read by name = %v on a later call, want %v every time, for %q", again, c.want, c.files)
				}
			}
			if encodesSecret(t, c.files) && !c.want {
				t.Errorf("html/template writes the unnamed Secret field, so want must be true, for %q", c.files)
			}
		})
	}
}
