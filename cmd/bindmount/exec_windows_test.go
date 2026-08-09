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

// ---------------------------------------------------------------------------
// injectPowerShellNoHistory
// ---------------------------------------------------------------------------

const psSetup = "Import-Module PSReadLine; Set-PSReadLineOption -HistorySaveStyle SaveNothing;"

func TestInjectPowerShellNoHistory(t *testing.T) {
	t.Run("non-pwsh unchanged", func(t *testing.T) {
		args := []string{"cmd.exe", "/c", "dir"}
		got := injectPowerShellNoHistory(args)
		if len(got) != len(args) || got[0] != args[0] {
			t.Errorf("non-pwsh args modified: %v", got)
		}
	})

	t.Run("empty unchanged", func(t *testing.T) {
		got := injectPowerShellNoHistory(nil)
		if got != nil {
			t.Errorf("nil args modified: %v", got)
		}
	})

	t.Run("interactive pwsh gets NoExit+setup", func(t *testing.T) {
		args := []string{"pwsh.exe"}
		got := injectPowerShellNoHistory(args)
		// Must append -NoExit -Command <setup>
		if len(got) < 3 {
			t.Fatalf("too few args: %v", got)
		}
		last := got[len(got)-1]
		if !strings.Contains(last, "SaveNothing") {
			t.Errorf("setup not appended; last arg = %q", last)
		}
		if got[len(got)-3] != "-NoExit" {
			t.Errorf("-NoExit not present: %v", got)
		}
	})

	t.Run("-Command prefix gets setup prepended", func(t *testing.T) {
		args := []string{"pwsh.exe", "-Command", "Get-Date"}
		got := injectPowerShellNoHistory(args)
		if len(got) != 3 {
			t.Fatalf("unexpected arg count %d: %v", len(got), got)
		}
		if !strings.HasPrefix(got[2], psSetup) {
			t.Errorf("setup not prepended to -Command value: %q", got[2])
		}
		if !strings.Contains(got[2], "Get-Date") {
			t.Errorf("original command lost: %q", got[2])
		}
	})

	t.Run("-c alias for -Command", func(t *testing.T) {
		args := []string{"pwsh.exe", "-c", "Write-Host hi"}
		got := injectPowerShellNoHistory(args)
		if !strings.HasPrefix(got[2], psSetup) {
			t.Errorf("setup not prepended for -c alias: %q", got[2])
		}
	})

	t.Run("-File rewritten as -Command with quoted script", func(t *testing.T) {
		args := []string{"pwsh.exe", "-File", "build.ps1", "-Arg", "val"}
		got := injectPowerShellNoHistory(args)
		// The -File/-f switch must be replaced with -Command.
		found := false
		for _, a := range got {
			if strings.EqualFold(a, "-Command") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("-Command not present after -File rewrite: %v", got)
		}
		cmd := got[len(got)-1]
		if !strings.Contains(cmd, "build.ps1") {
			t.Errorf("script name lost in -File rewrite: %q", cmd)
		}
	})

	t.Run("-EncodedCommand left unchanged", func(t *testing.T) {
		enc := "RwBlAHQALQBEAGEAdABlAA=="
		args := []string{"pwsh.exe", "-EncodedCommand", enc}
		got := injectPowerShellNoHistory(args)
		if len(got) != 3 || got[2] != enc {
			t.Errorf("-EncodedCommand args modified: %v", got)
		}
	})

	t.Run("pwsh.exe path prefix stripped for recognition", func(t *testing.T) {
		args := []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-Command", "1+1"}
		got := injectPowerShellNoHistory(args)
		if !strings.HasPrefix(got[2], psSetup) {
			t.Errorf("pwsh not recognised by full path: %q", got[2])
		}
	})
}
