package notify

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommand_UnsupportedPlatform(t *testing.T) {
	if cmd := command(context.Background(), "plan9", "t", "b", time.Second); cmd != nil {
		t.Fatal("expected nil command on unsupported platform")
	}
}

func TestCommand_PerPlatform(t *testing.T) {
	hostile := "It's \"quoted\" $(rm -rf /) `backtick` \\ end"
	cases := []struct {
		goos       string
		program    string
		wantInArgs []string
		wantInLast []string
	}{
		{
			goos:       "linux",
			program:    "notify-send",
			wantInArgs: []string{"--expire-time=90000", hostile},
		},
		{
			goos:    "darwin",
			program: "osascript",
			// Quotes and backslashes escaped, everything else literal.
			wantInLast: []string{`\"quoted\"`, `\\ end`, "$(rm -rf /)"},
		},
		{
			goos:    "windows",
			program: "powershell",
			// Single quotes doubled inside a single-quoted literal.
			wantInLast: []string{"'It''s", "$(rm -rf /)", "Start-Sleep -Milliseconds 15000"},
		},
	}
	for _, c := range cases {
		cmd := command(context.Background(), c.goos, hostile, "Body", 90*time.Second)
		if cmd == nil || cmd.Args[0] != c.program {
			t.Fatalf("%s: program = %v, want %s", c.goos, cmd, c.program)
		}
		for _, want := range c.wantInArgs {
			if !slices.Contains(cmd.Args, want) {
				t.Errorf("%s: args %v missing %q", c.goos, cmd.Args, want)
			}
		}
		last := cmd.Args[len(cmd.Args)-1]
		for _, want := range c.wantInLast {
			if !strings.Contains(last, want) {
				t.Errorf("%s: script %q missing %q", c.goos, last, want)
			}
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\r\nb\n\n  c\td"); got != "a b c d" {
		t.Fatalf("oneLine = %q", got)
	}
}
