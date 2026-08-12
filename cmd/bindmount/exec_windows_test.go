//go:build windows

package main

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// splitLinkSpec
// ---------------------------------------------------------------------------

func TestSplitLinkSpec(t *testing.T) {
	cases := []struct {
		in       string
		root     string
		target   string
		readOnly bool
		merged   bool
		ok       bool
	}{
		// Basic writable mapping.
		{`C:\v=C:\t`, `C:\v`, `C:\t`, false, false, true},
		// Read-only mapping (double equals).
		{`C:\v==C:\t`, `C:\v`, `C:\t`, true, false, true},
		// Merged writable mapping.
		{`C:\v+=C:\t`, `C:\v`, `C:\t`, false, true, true},
		// Merged read-only mapping.
		{`C:\v+==C:\t`, `C:\v`, `C:\t`, true, true, true},
		// File-level mapping (extensions in both paths).
		{`C:\foo\a.exe=C:\bar\b.exe`, `C:\foo\a.exe`, `C:\bar\b.exe`, false, false, true},
		// Target contains an equals sign — only the first = is the separator.
		{`C:\v=C:\t=x`, `C:\v`, `C:\t=x`, false, false, true},
		// Empty input — not OK.
		{``, ``, ``, false, false, false},
		// No separator — not OK.
		{`C:\foo`, ``, ``, false, false, false},
		// Empty root (separator is first character) — not OK.
		{`=C:\t`, ``, ``, false, false, false},
		// Empty target (separator is last character) — not OK.
		{`C:\v=`, ``, ``, false, false, false},
		// Double-equals with empty target — not OK.
		{`C:\v==`, ``, ``, false, false, false},
		// Paths with spaces.
		{`C:\Program Files\app=C:\Backup\app`, `C:\Program Files\app`, `C:\Backup\app`, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			root, target, readOnly, merged, ok := splitLinkSpec(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if root != c.root {
				t.Errorf("root = %q, want %q", root, c.root)
			}
			if target != c.target {
				t.Errorf("target = %q, want %q", target, c.target)
			}
			if readOnly != c.readOnly {
				t.Errorf("readOnly = %v, want %v", readOnly, c.readOnly)
			}
			if merged != c.merged {
				t.Errorf("merged = %v, want %v", merged, c.merged)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildCommandLine / quoteArg
// ---------------------------------------------------------------------------

func TestQuoteArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// No special characters — returned as-is.
		{"simple", "simple"},
		{"C:\\Windows\\System32", "C:\\Windows\\System32"},
		// Space triggers quoting.
		{"hello world", `"hello world"`},
		// Tab triggers quoting.
		{"a\tb", "\"a\tb\""},
		// Embedded double-quote: the quote is escaped with a backslash.
		{`say "hi"`, `"say \"hi\""`},
		// Trailing backslash on a path that needs quoting (has a space): the
		// backslash must be doubled so it does not escape the closing quote.
		{`C:\foo bar\`, `"C:\foo bar\\"`},
		// Backslashes before a quote: each must be doubled, then the quote escaped.
		{`a\"b`, `"a\\\"b"`},
		// Empty string needs quoting.
		{"", `""`},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := quoteArg(c.in)
			if got != c.want {
				t.Errorf("quoteArg(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestBuildCommandLine(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"cmd.exe"}, "cmd.exe"},
		{[]string{"cmd.exe", "/c", "dir"}, "cmd.exe /c dir"},
		{[]string{"pwsh.exe", "-Command", "Get-Date"}, "pwsh.exe -Command Get-Date"},
		// Argument with spaces is quoted; other args are not.
		{[]string{"tool.exe", "path with space", "plain"}, `tool.exe "path with space" plain`},
		// Empty slice produces empty string.
		{[]string{}, ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			got := buildCommandLine(c.args)
			if got != c.want {
				t.Errorf("buildCommandLine(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

