package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPassword(t *testing.T) {
	dir := t.TempDir()
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, c := range map[string]struct {
		path, stdin, want string
	}{
		"file":                {path: file("plain", "correct horse battery"), want: "correct horse battery"},
		"one newline dropped": {path: file("newline", "pass with space \n"), want: "pass with space "},
		"crlf dropped":        {path: file("crlf", "windows-line-ending\r\n"), want: "windows-line-ending"},
		"stdin":               {path: "-", stdin: "from-stdin-password\n", want: "from-stdin-password"},
	} {
		got, err := readPassword(c.path, strings.NewReader(c.stdin))
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, c.want)
		}
	}
	for name, c := range map[string]struct{ path, stdin string }{
		"empty":     {path: "-", stdin: "\n"},
		"missing":   {path: filepath.Join(dir, "missing")},
		"oversized": {path: "-", stdin: strings.Repeat("a", maxPasswordFile+1)},
	} {
		if _, err := readPassword(c.path, strings.NewReader(c.stdin)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
