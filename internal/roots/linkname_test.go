package roots

import "testing"

func TestLinknameDirectiveReadsBothNames(t *testing.T) {
	cases := map[string]struct {
		text       string
		wantLocal  string
		wantRemote string
		wantOK     bool
	}{
		"one argument":                  {text: "//go:linkname pushed", wantLocal: "pushed", wantOK: true},
		"two arguments":                 {text: "//go:linkname pulled example.com/other.f", wantLocal: "pulled", wantRemote: "example.com/other.f", wantOK: true},
		"the standard library's form":   {text: "//go:linknamestd pushed", wantLocal: "pushed", wantOK: true},
		"a remote name in another form": {text: "//go:linkname local runtime.nanotime", wantLocal: "local", wantRemote: "runtime.nanotime", wantOK: true},
		"a remote name with no path":    {text: "//go:linkname local nanotime", wantLocal: "local", wantRemote: "nanotime", wantOK: true},
		"tabs between the words":        {text: "//go:linkname\tlocal\texample.com/other.f", wantLocal: "local", wantRemote: "example.com/other.f", wantOK: true},
		"three arguments":               {text: "//go:linkname a b c"},
		"no argument":                   {text: "//go:linkname"},
		"another directive":             {text: "//go:embed catalog.json"},
		"a space before the name":       {text: "// go:linkname pushed"},
		// The two spellings are matched whole: a longer word that starts with one
		// of them is a different directive, or prose, and binds nothing.
		"a spelling extended by a letter":    {text: "//go:linknamed local"},
		"a spelling extended by two letters": {text: "//go:linknamestdx local"},
		"a spelling cut short":               {text: "//go:linknam local"},
		"prose naming the directive":         {text: "// The //go:linkname directive names a symbol."},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			local, remote, ok := linknameDirective(test.text)
			if local != test.wantLocal || remote != test.wantRemote || ok != test.wantOK {
				t.Errorf("linknameDirective(%q) = %q, %q, %t, want %q, %q, %t",
					test.text, local, remote, ok, test.wantLocal, test.wantRemote, test.wantOK)
			}
		})
	}
}
