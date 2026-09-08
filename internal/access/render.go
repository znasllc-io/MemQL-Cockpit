package access

import (
	"fmt"
	"io"
	"strings"
)

// ===========================================================================
// WHAT THIS SURFACE IS FOR
// ===========================================================================
// It answers one question -- *what am I on this cluster, and why was I just
// refused?* -- and the reason it is a command rather than a portal page is
// that the portal renders what the CLUSTER believes about a browser session,
// while a person hitting a refusal from this machine wants what the cluster
// believes about THIS credential. Those differ exactly when it matters: a
// stale token, a PAT with a different ceiling, a second account.
//
// ===========================================================================
// AN ABSENCE IS A SENTENCE, NEVER A BLANK
// ===========================================================================
// Every field here has a state where the cluster said nothing, and a blank or
// a dash reads as the OPPOSITE claim -- "you hold no role" instead of "this
// cluster did not say". The first sends somebody to ask for a grant they
// already have. So each absence prints why it is absent, and the two
// never render alike. It is the same rule probe.Figure keeps for a
// measurement that could not be taken.

// labelWidth aligns the value column. Wide enough for the longest label
// ("Account scope") plus breathing room.
const labelWidth = 16

// Render writes the human-readable access report.
func Render(w io.Writer, s Summary, cluster string) {
	fmt.Fprintf(w, "Your access on %q\n\n", cluster)

	fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Signed in as", signedInAs(s))
	if s.UserID != "" {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "User id", s.UserID)
	}
	fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Session", sessionLine(s))
	fmt.Fprintln(w, "")

	renderRole(w, s.Role)
	renderGroups(w, s.Groups)
	renderScope(w, s.Scope)

	if notes := pendingNotes(s); len(notes) > 0 {
		fmt.Fprintln(w, "")
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
	}
}

func signedInAs(s Summary) string {
	switch {
	case s.DisplayName != "" && s.PrimaryEmail != "":
		return fmt.Sprintf("%s <%s>", s.DisplayName, s.PrimaryEmail)
	case s.PrimaryEmail != "":
		return s.PrimaryEmail
	case s.DisplayName != "":
		return s.DisplayName
	default:
		// The resolver produced no user row. Not an error -- a first request
		// racing the registration insert looks like this -- but it is worth
		// naming, because every other line will look thin for the same reason.
		return "(the cluster resolved no user row for this credential)"
	}
}

func sessionLine(s Summary) string {
	if s.SessionID == "" {
		// A PAT, an operator key or a service account has no session row to
		// name. MyAccessResult's own comment calls this out as not an error.
		return "none — this credential carries no session"
	}
	return s.SessionID + "  (this device)"
}

func renderRole(w io.Writer, r Role) {
	if !r.Reported {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Role", absent)
		return
	}
	fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Role", roleHeadline(r))
	if detail := roleDetail(r); detail != "" {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "", detail)
	}
}

// roleHeadline is the name when there is one, else the slug. A custom role
// whose catalog row has no display name still has to render as itself.
func roleHeadline(r Role) string {
	if r.Name != "" {
		return r.Name
	}
	if r.Slug != "" {
		return r.Slug
	}
	return "(the cluster reported an empty role)"
}

// roleDetail is the slug-and-rank line under the headline. The slug is
// repeated when a name stood in for it above, because the slug is what every
// grant, invitation and delegation ceiling is written in terms of.
func roleDetail(r Role) string {
	parts := make([]string, 0, 2)
	if r.Name != "" && r.Slug != "" {
		parts = append(parts, r.Slug)
	}
	if r.HasRank {
		parts = append(parts, rankPhrase(r.Rank))
	}
	return strings.Join(parts, " · ")
}

// rankPhrase spells the rank, and explains it AT rank 0 rather than on a line
// of its own.
//
// Rank 0 is the design record's unknown slug: the resolver treats the holder
// as holding nothing, everywhere, until they are re-roled -- the documented
// consequence of the cluster-owner write escape. It is the single fact that
// explains every refusal the reader is about to hit, so a bare "rank 0" buries
// it. Saying it twice is no better: two lines both beginning "rank 0" read as
// two different facts about two different things.
func rankPhrase(rank int32) string {
	if rank == 0 {
		return "rank 0 — this role holds no permissions on this cluster"
	}
	return fmt.Sprintf("rank %d", rank)
}

func renderGroups(w io.Writer, g Groups) {
	if !g.Reported {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Groups", absent)
		return
	}
	if len(g.Items) == 0 {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Groups", "none")
		return
	}
	for i, item := range g.Items {
		label := "Groups"
		if i > 0 {
			label = ""
		}
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, label, groupLine(item))
	}
}

func groupLine(g Group) string {
	line := g.Name
	if line == "" {
		line = g.ID
	}
	if g.Kind != "" {
		line += " (" + g.Kind + ")"
	}
	if g.AccountName != "" {
		line += " — " + g.AccountName
	} else if g.AccountID != "" {
		line += " — " + g.AccountID
	}
	return line
}

func renderScope(w io.Writer, s Scope) {
	if !s.Reported {
		fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Account scope", absent)
		return
	}
	fmt.Fprintf(w, "  %-*s%s\n", labelWidth, "Account scope", scopeLine(s))
}

func scopeLine(s Scope) string {
	if s.EveryAccount {
		// Record A's D6: account_ids is empty AND the bool is set. Rendering
		// that as "none" would be the exact inverse of the truth.
		return "every account on this cluster"
	}
	if len(s.AccountIDs) == 0 {
		return "none"
	}
	return strings.Join(s.AccountIDs, ", ")
}

const absent = "not reported by this cluster"

// pendingNotes explains the absences, once, at the bottom -- rather than
// repeating an engine issue number beside three separate lines.
func pendingNotes(s Summary) []string {
	var notes []string
	if !s.Role.Reported {
		notes = append(notes,
			"This cluster does not report roles as data yet. The engine change that adds",
			"the role slug, its name and its rank to MyAccess is memql#5181; until that",
			"lands there is nothing here to show. The cockpit does NOT fall back to the",
			"retired five-value enum: a custom role is none of those five, so a guess",
			"would be wrong in exactly the case this command exists for.")
	}
	if !s.Groups.Reported || !s.Scope.Reported {
		if len(notes) > 0 {
			notes = append(notes, "")
		}
		notes = append(notes,
			"Group membership and account scope arrive with memql#5165.")
	}
	return notes
}
