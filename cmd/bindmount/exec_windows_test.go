//go:build windows

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRootHelpDescribesCommand(t *testing.T) {
	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Manage global or silo-scoped Windows Bind Filter mappings",
		"Most mapping and silo operations require elevation",
		"Available Commands:",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("root help does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestShouldShowSkippedMappingWarnings(t *testing.T) {
	cases := []struct {
		detach, verbose, want bool
	}{
		{false, false, true},
		{false, true, true},
		{true, false, false},
		{true, true, true},
	}
	for _, c := range cases {
		if got := shouldShowSkippedMappingWarnings(c.detach, c.verbose); got != c.want {
			t.Errorf("detach=%v verbose=%v: got %v, want %v", c.detach, c.verbose, got, c.want)
		}
	}
}

func TestWarnSkippedMapping(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = oldStderr
		r.Close()
	})

	warnSkippedMapping(false, "suppressed", `C:\hidden`, `D:\hidden`, "not visible")
	warnSkippedMapping(true, "user", `C:\foo`, `D:\one`, "virtual root is already mapped")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = oldStderr

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := "bindmount: warning: skipping user mapping C:\\foo -> D:\\one: virtual root is already mapped\n"
	if string(got) != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

func TestPowerShellMappingPathsSkipMissingEnvironmentVariables(t *testing.T) {
	items := powerShellMappingPaths("", `C:\Users\test\AppData\Local`)
	if len(items) != 1 {
		t.Fatalf("mapping count = %d, want 1", len(items))
	}
	if items[0].name != "powershell-local" {
		t.Fatalf("mapping name = %q, want powershell-local", items[0].name)
	}
	if !filepath.IsAbs(items[0].path) {
		t.Fatalf("mapping path is relative: %q", items[0].path)
	}

	items = powerShellMappingPaths(`C:\Users\test\AppData\Roaming`, "")
	if len(items) != 1 || items[0].name != "powershell-history" {
		t.Fatalf("mappings = %#v, want only powershell-history", items)
	}
	if !filepath.IsAbs(items[0].path) {
		t.Fatalf("mapping path is relative: %q", items[0].path)
	}
}

func TestProfileRelativeForDrive(t *testing.T) {
	if got, ok := profileRelativeForDrive(`D:\Users\test`, 'D'); !ok || got != `Users\test` {
		t.Fatalf("D: profile = %q, %v; want Users\\test, true", got, ok)
	}
	if got, ok := profileRelativeForDrive(`D:\Users\test`, 'C'); ok || got != "" {
		t.Fatalf("D: profile on C: = %q, %v; want empty, false", got, ok)
	}
	if got, ok := profileRelativeForDrive(`\\server\share\Users\test`, 'C'); ok || got != "" {
		t.Fatalf("UNC profile = %q, %v; want empty, false", got, ok)
	}
}

func TestStageDirectorySymbolicLink(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "nvm4w", "nodejs")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("create directory symbolic link: %v", err)
	}

	linkTarget, resolved, isLink, err := directorySymbolicLink(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !isLink {
		t.Fatal("directory symbolic link was not detected")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		t.Fatalf("stat resolved target: %v", err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat expected target: %v", err)
	}
	if !os.SameFile(resolvedInfo, targetInfo) {
		t.Fatalf("resolved target = %q, want directory %q", resolved, target)
	}

	backingRoot := t.TempDir()
	stagedPath, err := stageDirectorySymbolicLink(backingRoot, alias, linkTarget)
	if err != nil {
		t.Fatal(err)
	}
	wantStagedPath, err := rootBackingPath(backingRoot, alias)
	if err != nil {
		t.Fatal(err)
	}
	if stagedPath != wantStagedPath {
		t.Fatalf("staged path = %q, want %q", stagedPath, wantStagedPath)
	}
	stagedTarget, err := os.Readlink(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(stagedTarget, linkTarget) {
		t.Fatalf("staged target = %q, want %q", stagedTarget, linkTarget)
	}
	if _, err := stageDirectorySymbolicLink(backingRoot, alias, linkTarget); err != nil {
		t.Fatalf("reuse staged symbolic link: %v", err)
	}

	replacementTarget := t.TempDir()
	if _, err := stageDirectorySymbolicLink(backingRoot, alias, replacementTarget); err != nil {
		t.Fatalf("replace staged symbolic link: %v", err)
	}
	stagedTarget, err = os.Readlink(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(stagedTarget, replacementTarget) {
		t.Fatalf("replaced target = %q, want %q", stagedTarget, replacementTarget)
	}
}

func TestSiloLookupNames(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"demo", []string{"demo", `Global\demo`}},
		{`Global\demo`, []string{`Global\demo`}},
		{`global\demo`, []string{`global\demo`}},
		{`Local\demo`, []string{`Local\demo`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := siloLookupNames(c.name)
			if !slices.Equal(got, c.want) {
				t.Fatalf("siloLookupNames(%q) = %#v, want %#v", c.name, got, c.want)
			}
		})
	}
}

func TestExecRequiresCommandSeparator(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "cmd.exe"},
		{"--link", `C:\a=C:\b`, "demo", "cmd.exe"},
	} {
		err := cmdExecInner(args)
		if err == nil || !strings.Contains(err.Error(), `requires "--"`) {
			t.Fatalf("cmdExecInner(%q) error = %v, want missing separator error", args, err)
		}
	}
}

func TestExecHelpDescribesNoUIRestrictions(t *testing.T) {
	if !strings.Contains(execUsage, "--no-ui-restrictions") {
		t.Fatalf("exec usage does not describe --no-ui-restrictions: %s", execUsage)
	}
}

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
