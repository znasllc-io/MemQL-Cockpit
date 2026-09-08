package access

import (
	"strings"
	"testing"
)

func runCmd(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e strings.Builder
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

// An asked-for help text is a SUCCESS. Exiting non-zero for it breaks
// `memql access --help && ...` and makes the command look broken in a script.
func TestHelpExitsZero(t *testing.T) {
	code, _, errOut := runCmd(t, "--help")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(errOut, "Usage: memql access") {
		t.Errorf("help text not printed:\n%s", errOut)
	}
}

// A flag that does not exist is a usage error, which is a different thing.
func TestUnknownFlagExitsTwo(t *testing.T) {
	code, _, _ := runCmd(t, "--not-a-flag")
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// With no clusters registered the command fails with the repair, and prints
// nothing at all on stdout -- a script piping this into jq must not get half
// a document.
func TestNoClustersFailsWithTheRepairAndPrintsNothingOnStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	code, out, errOut := runCmd(t)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "memql cluster add") {
		t.Errorf("stderr does not name the repair:\n%s", errOut)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}
