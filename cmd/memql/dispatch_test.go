package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func mainSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	return string(body)
}

// usageBody returns just printUsage's body, so "the usage text mentions it"
// cannot be satisfied by a mention somewhere else in the file.
func usageBody(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "func printUsage()")
	if start < 0 {
		t.Fatal("main.go has no printUsage()")
	}
	rest := src[start:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// `memql access` is memql-cockpit#403's surface. A command that dispatches but
// is not in the usage text is a command nobody finds.
func TestAccessIsDispatchedAndDocumented(t *testing.T) {
	src := mainSource(t)
	if !strings.Contains(src, `case "access":`) {
		t.Error("main.go does not dispatch `access`")
	}
	if !strings.Contains(usageBody(t, src), "memql access") {
		t.Error("printUsage does not list `memql access`")
	}
}

// The general rule behind the test above: every verb the switch answers to is
// a verb the usage text names. The two drift apart in one direction silently --
// a command that works and is undiscoverable -- and this is the cheap way to
// notice.
func TestEveryDispatchedCommandAppearsInTheUsage(t *testing.T) {
	src := mainSource(t)
	usage := usageBody(t, src)

	// The flag spellings and `help` itself are aliases, not commands.
	aliases := map[string]bool{
		"-v": true, "--version": true, "-h": true, "--help": true, "help": true,
	}

	verbs := regexp.MustCompile(`case "([a-z-]+)"`).FindAllStringSubmatch(src, -1)
	if len(verbs) < 5 {
		t.Fatalf("found only %d dispatch arms; the regexp is not matching main.go's switch", len(verbs))
	}
	seen := map[string]bool{}
	for _, m := range verbs {
		verb := m[1]
		if aliases[verb] || seen[verb] {
			continue
		}
		seen[verb] = true
		if !strings.Contains(usage, verb) {
			t.Errorf("`memql %s` is dispatched but never named in printUsage", verb)
		}
	}
}
