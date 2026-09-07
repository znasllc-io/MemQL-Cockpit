package hardware

import "testing"

// Version output is not a format anybody promised. These are the real
// greetings, captured, and the parser has to survive all of them --
// including the one that carries no version at all, which reports
// "present, version unknown" rather than lying about a number.
func TestParseRuntimeVersion(t *testing.T) {
	for _, tc := range []struct{ name, out, want string }{
		{"mflux", "mflux-generate, version 0.9.1\n", "0.9.1"},
		{"whisper banner", "whisper-cli version 1.7.2 (build 4a9f)\n", "1.7.2"},
		{"bare number", "0.13.0\n", "0.13.0"},
		{"two segments", "v1.7\n", "1.7"},
		{"four segments", "tool 2.1.0.3\n", "2.1.0.3"},
		{"no version anywhere", "usage: whisper-cli [options]\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRuntimeVersion(tc.out); got != tc.want {
				t.Fatalf("parseRuntimeVersion(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

// The docker probe reads the SERVER version, so its output is one line
// and firstLine must not fold a trailing warning into it.
func TestFirstLine(t *testing.T) {
	got := firstLine("27.3.1\nWARNING: daemon is in swarm mode\n")
	if got != "27.3.1" {
		t.Fatalf("firstLine = %q, want 27.3.1", got)
	}
}

// The Kokoro base URL is overridable, and the override is trimmed of a
// trailing slash so the probe does not build "//health".
func TestKokoroBaseURL(t *testing.T) {
	t.Setenv("MEMQL_KOKORO_HOST", "")
	if got := KokoroBaseURL(); got != DefaultKokoroBaseURL {
		t.Fatalf("default = %q, want %q", got, DefaultKokoroBaseURL)
	}
	t.Setenv("MEMQL_KOKORO_HOST", "http://box.local:9000/")
	if got := KokoroBaseURL(); got != "http://box.local:9000" {
		t.Fatalf("override = %q, want the trimmed URL", got)
	}
}

// detectRuntimes must produce a byte-identical payload for an unchanged
// machine: the list is sorted, so map iteration order cannot rewrite a
// registration row on every refresh.
func TestDetectRuntimesIsSorted(t *testing.T) {
	rts := []Runtime{{Name: "ollama"}, {Name: "docker"}, {Name: "kokoro"}}
	sortRuntimes(rts)
	want := []string{"docker", "kokoro", "ollama"}
	for i, r := range rts {
		if r.Name != want[i] {
			t.Fatalf("sorted[%d] = %q, want %q", i, r.Name, want[i])
		}
	}
}
