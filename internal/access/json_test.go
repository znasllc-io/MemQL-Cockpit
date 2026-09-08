package access

import (
	"encoding/json"
	"strings"
	"testing"
)

func renderJSON(t *testing.T, s Summary, cluster string) map[string]any {
	t.Helper()
	var b strings.Builder
	if err := RenderJSON(&b, s, cluster); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	return got
}

// A SCRIPT has to be able to tell "no role" from "the cluster never said", for
// the same reason a reader does -- and JSON makes it easier to get wrong,
// because a missing key and a false one both read as falsey.
func TestRenderJSONMarksAnUnreportedRoleAsUnreportedRatherThanOmittingIt(t *testing.T) {
	got := renderJSON(t, Summary{UserID: "u_ada"}, "acme")

	role, ok := got["role"].(map[string]any)
	if !ok {
		t.Fatalf("role is missing from the JSON; a script cannot tell absence from empty: %#v", got)
	}
	if reported, _ := role["reported"].(bool); reported {
		t.Errorf("role.reported = true, want false")
	}
	if _, present := role["slug"]; present {
		t.Errorf("role.slug is present for an unreported role: %#v", role)
	}
}

func TestRenderJSONCarriesTheSlugNameAndRank(t *testing.T) {
	got := renderJSON(t, Summary{
		UserID: "u_ada",
		Role: Role{
			Slug: "release-manager", Name: "Release Manager",
			Rank: 150, HasRank: true, Reported: true,
		},
	}, "acme")

	role := got["role"].(map[string]any)
	if reported, _ := role["reported"].(bool); !reported {
		t.Errorf("role.reported = false, want true")
	}
	if role["slug"] != "release-manager" {
		t.Errorf("role.slug = %v, want release-manager", role["slug"])
	}
	if role["name"] != "Release Manager" {
		t.Errorf("role.name = %v, want Release Manager", role["name"])
	}
	if rank, _ := role["rank"].(float64); rank != 150 {
		t.Errorf("role.rank = %v, want 150", role["rank"])
	}
	if got["cluster"] != "acme" {
		t.Errorf("cluster = %v, want acme", got["cluster"])
	}
}

// Rank 0 must survive the round trip as a REPORTED zero, not vanish through
// JSON's omitempty the way a bare int would.
func TestRenderJSONKeepsAnExplicitRankOfZero(t *testing.T) {
	got := renderJSON(t, Summary{
		Role: Role{Slug: "retired-role", Rank: 0, HasRank: true, Reported: true},
	}, "acme")

	role := got["role"].(map[string]any)
	rank, present := role["rank"]
	if !present {
		t.Fatalf("role.rank is absent for an explicit rank of 0: %#v", role)
	}
	if v, _ := rank.(float64); v != 0 {
		t.Errorf("role.rank = %v, want 0", rank)
	}
}
