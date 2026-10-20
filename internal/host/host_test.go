package host

import "testing"

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":            "''",
		"simple":      "simple",
		"/etc/x.conf": "/etc/x.conf",
		"a b":         "'a b'",
		"it's":        `'it'\''s'`,
		"$HOME":       "'$HOME'",
		"K=v":         "K=v",
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ShellJoin([]string{"echo", "hi there"}); got != "echo 'hi there'" {
		t.Errorf("ShellJoin = %q", got)
	}
}
