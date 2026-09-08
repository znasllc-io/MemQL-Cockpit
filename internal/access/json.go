package access

import (
	"encoding/json"
	"io"
)

// ===========================================================================
// THE MACHINE-READABLE HALF OF "AN ABSENCE IS NOT AN EMPTY VALUE"
// ===========================================================================
// JSON makes the confusion EASIER than prose does, because a missing key and a
// false one are both falsey to every caller that reads them. A script asking
// "may this machine deploy?" must not read a cluster that never mentioned
// roles as a person who holds none.
//
// So every block that can be absent is an OBJECT with an explicit `reported`,
// and its contents are omitted entirely when `reported` is false -- there is no
// shape in which a caller sees `"slug": ""` and has to guess which it means.
//
// `rank` is a POINTER for the same reason it carries HasRank in Go: 0 is a real
// rank meaning "holds nothing", and `omitempty` on a plain int would delete
// precisely the value a caller most needs to see.

type jsonSummary struct {
	Cluster      string     `json:"cluster"`
	UserID       string     `json:"user_id,omitempty"`
	PrimaryEmail string     `json:"primary_email,omitempty"`
	DisplayName  string     `json:"display_name,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	Role         jsonRole   `json:"role"`
	Groups       jsonGroups `json:"groups"`
	Scope        jsonScope  `json:"account_scope"`
}

type jsonRole struct {
	Reported bool   `json:"reported"`
	Slug     string `json:"slug,omitempty"`
	Name     string `json:"name,omitempty"`
	Rank     *int32 `json:"rank,omitempty"`
}

type jsonGroups struct {
	Reported bool        `json:"reported"`
	Items    []jsonGroup `json:"items,omitempty"`
}

type jsonGroup struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
}

type jsonScope struct {
	Reported     bool     `json:"reported"`
	AccountIDs   []string `json:"account_ids,omitempty"`
	EveryAccount *bool    `json:"every_account,omitempty"`
}

// RenderJSON writes the access report as JSON.
func RenderJSON(w io.Writer, s Summary, cluster string) error {
	out := jsonSummary{
		Cluster:      cluster,
		UserID:       s.UserID,
		PrimaryEmail: s.PrimaryEmail,
		DisplayName:  s.DisplayName,
		SessionID:    s.SessionID,
		Role:         jsonRole{Reported: s.Role.Reported},
		Groups:       jsonGroups{Reported: s.Groups.Reported},
		Scope:        jsonScope{Reported: s.Scope.Reported},
	}
	if s.Role.Reported {
		out.Role.Slug = s.Role.Slug
		out.Role.Name = s.Role.Name
		if s.Role.HasRank {
			rank := s.Role.Rank
			out.Role.Rank = &rank
		}
	}
	if s.Groups.Reported {
		for _, g := range s.Groups.Items {
			out.Groups.Items = append(out.Groups.Items, jsonGroup(g))
		}
	}
	if s.Scope.Reported {
		out.Scope.AccountIDs = s.Scope.AccountIDs
		every := s.Scope.EveryAccount
		out.Scope.EveryAccount = &every
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
