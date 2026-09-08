package access

import (
	"strings"
	"testing"
)

// The two goldens below are the SURFACE, pinned whole rather than sentence by
// sentence. Alignment, blank lines and ordering are the part a reader actually
// navigates by, and none of them is covered by asserting that a phrase appears
// somewhere. Changing either block should be a deliberate act.

// What every operator sees TODAY, against any cluster at the current pin.
// This is the state the command spends its whole early life in, so it is the
// one worth being sure about.
const goldenPinnedCluster = `Your access on "acme"

  Signed in as    Ada Lovelace <ada@example.com>
  User id         u_01JQ8ZK
  Session         sess_01JQ90F  (this device)

  Role            not reported by this cluster
  Groups          not reported by this cluster
  Account scope   not reported by this cluster

This cluster does not report roles as data yet. The engine change that adds
the role slug, its name and its rank to MyAccess is memql#5181; until that
lands there is nothing here to show. The cockpit does NOT fall back to the
retired five-value enum: a custom role is none of those five, so a guess
would be wrong in exactly the case this command exists for.

Group membership and account scope arrive with memql#5165.
`

// The same machine once memql#5181 and memql#5165 have landed, holding a
// CUSTOM role -- the case the epic exists for.
const goldenCustomRole = `Your access on "acme"

  Signed in as    Ada Lovelace <ada@example.com>
  User id         u_01JQ8ZK
  Session         sess_01JQ90F  (this device)

  Role            Release Manager
                  release-manager · rank 150
  Groups          Platform (team) — Acme Corp
                  Release Captains (role-group) — Acme Corp
  Account scope   acct_9
`

func TestGoldenAgainstAClusterThatDoesNotReportRolesYet(t *testing.T) {
	var b strings.Builder
	Render(&b, Summary{
		UserID: "u_01JQ8ZK", PrimaryEmail: "ada@example.com",
		DisplayName: "Ada Lovelace", SessionID: "sess_01JQ90F",
	}, "acme")
	assertGolden(t, b.String(), goldenPinnedCluster)
}

func TestGoldenWithACustomRoleGroupsAndScope(t *testing.T) {
	var b strings.Builder
	Render(&b, Summary{
		UserID: "u_01JQ8ZK", PrimaryEmail: "ada@example.com",
		DisplayName: "Ada Lovelace", SessionID: "sess_01JQ90F",
		Role: Role{
			Slug: "release-manager", Name: "Release Manager",
			Rank: 150, HasRank: true, Reported: true,
		},
		Groups: Groups{Reported: true, Items: []Group{
			{ID: "g_1", Name: "Platform", Kind: "team", AccountID: "acct_9", AccountName: "Acme Corp"},
			{ID: "g_2", Name: "Release Captains", Kind: "role-group", AccountID: "acct_9", AccountName: "Acme Corp"},
		}},
		Scope: Scope{Reported: true, AccountIDs: []string{"acct_9"}},
	}, "acme")
	assertGolden(t, b.String(), goldenCustomRole)
}

func assertGolden(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "", ""
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Errorf("line %d:\n  got  %q\n  want %q", i+1, g, w)
		}
	}
}
