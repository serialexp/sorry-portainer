package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToken(t *testing.T) {
	token, err := readToken("-", strings.NewReader("  spt1.abc\n"), nil)
	if err != nil || token != "spt1.abc" {
		t.Fatalf("stdin token %q %v", token, err)
	}
	// Without a trailing newline, as from a file written with printf.
	if token, err = readToken("-", strings.NewReader("spt1.abc"), nil); err != nil || token != "spt1.abc" {
		t.Fatalf("token without newline %q %v", token, err)
	}
	// Only the first line counts: pressing Enter after pasting ends the input.
	var prompt strings.Builder
	if token, err = readToken("-", strings.NewReader("spt1.abc\nrest\n"), &prompt); err != nil || token != "spt1.abc" {
		t.Fatalf("first line %q %v", token, err)
	}
	if !strings.Contains(prompt.String(), "Paste the enrollment token") {
		t.Fatalf("prompt %q", prompt.String())
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("spt1.def\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt.Reset()
	if token, err = readToken(path, nil, &prompt); err != nil || token != "spt1.def" || prompt.Len() != 0 {
		t.Fatalf("file token %q %v, prompt %q", token, err, prompt.String())
	}
	if _, err := readToken("-", strings.NewReader(" \n"), nil); err == nil {
		t.Fatal("accepted an empty token")
	}
	if _, err := readToken("-", strings.NewReader(strings.Repeat("a", maxTokenFile+1)), nil); err == nil {
		t.Fatal("accepted an oversized token file")
	}
}

func TestEnrollRequiresItsArguments(t *testing.T) {
	full := []string{"--server-url", "wss://m:9443/control/agent", "--host-id", "hel1", "--dir", t.TempDir(), "--token-file", "-"}
	for i := 0; i < len(full); i += 2 {
		args := append(append([]string{}, full[:i]...), full[i+2:]...)
		err := runEnroll(args, strings.NewReader("spt1.x"), nil, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), full[i]) {
			t.Errorf("without %s: %v", full[i], err)
		}
	}
	// The token is never taken from the command line.
	if err := runEnroll(append(full, "--token", "spt1.x"), strings.NewReader(""), nil, &strings.Builder{}); err == nil {
		t.Fatal("accepted --token")
	}
}
