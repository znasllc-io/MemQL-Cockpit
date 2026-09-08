// Package access reads the caller's own access record off a MemQL cluster
// and renders it: who the cluster thinks you are, what role you hold, and
// what that role's rank says about where it stands.
package access

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"
)

// Summary is what the cluster says about the caller.
type Summary struct {
	UserID       string
	PrimaryEmail string
	DisplayName  string
	SessionID    string
	Role         Role
	Groups       Groups
	Scope        Scope
}

// Group is one membership the cluster reports (memql#5165).
type Group struct {
	ID          string
	Name        string
	Kind        string
	AccountID   string
	AccountName string
}

// Groups is the caller's memberships, with Reported false when the cluster
// carried no `groups` field -- distinct from a caller who is in none.
type Groups struct {
	Items    []Group
	Reported bool
}

// Scope is the account scope the caller's memberships resolve to.
//
// EveryAccount is record A's D6 staff case: `account_ids` is EMPTY and the
// bool is set. An empty list with the bool clear is the opposite fact -- no
// scope at all -- so Reported keeps them apart.
type Scope struct {
	AccountIDs   []string
	EveryAccount bool
	Reported     bool
}

// Role is the caller's cluster-wide role as a CATALOG SLUG, the role's
// display name, and its rank.
//
// Reported is false when the cluster's MyAccessResult carried no `role`
// field at all. That is a different fact from a role whose slug is empty,
// and the renderer says something different about each.
type Role struct {
	Slug string
	Name string
	Rank int32
	// HasRank exists because 0 IS A RANK -- the design record gives an
	// unknown slug `rankOf` 0, meaning "holds nothing". A plain int32
	// cannot tell that apart from a rank the cluster never sent.
	HasRank  bool
	Reported bool
}

// Decode reads a MyAccessResult into a Summary.
func Decode(m *memqlv1.MyAccessResult) Summary {
	if m == nil {
		return Summary{}
	}
	s := Summary{
		UserID:       m.GetUserId(),
		PrimaryEmail: m.GetPrimaryEmail(),
		DisplayName:  m.GetDisplayName(),
		SessionID:    m.GetSessionId(),
	}
	decodePending(m.ProtoReflect(), &s)
	return s
}

// decodePending reads the fields the engine has not landed yet.
func decodePending(m protoreflect.Message, s *Summary) {
	if slug, ok := stringByName(m, fieldRole); ok {
		s.Role.Reported = true
		s.Role.Slug = slug
	}
	if name, ok := stringByName(m, fieldRoleName); ok {
		s.Role.Name = name
	}
	if rank, ok := int32ByName(m, fieldRank); ok {
		s.Role.HasRank = true
		s.Role.Rank = rank
	}
	if groups, ok := groupsByName(m, fieldGroups); ok {
		s.Groups.Reported = true
		s.Groups.Items = groups
	}
	if ids, ok := stringListByName(m, fieldAccountIDs); ok {
		s.Scope.Reported = true
		s.Scope.AccountIDs = ids
	}
	if every, ok := boolByName(m, fieldEveryAccount); ok {
		s.Scope.Reported = true
		s.Scope.EveryAccount = every
	}
}

// The field NAMES are the contract; see the comment on lookupByName.
const (
	fieldRole     = "role"
	fieldRoleName = "role_name"
	fieldRank     = "rank"

	fieldGroups       = "groups"
	fieldAccountIDs   = "account_ids"
	fieldEveryAccount = "every_account"

	// Inside MyAccessGroup.
	fieldID          = "id"
	fieldName        = "name"
	fieldKind        = "kind"
	fieldAccountID   = "account_id"
	fieldAccountName = "account_name"
)

// stringByName reads a string field BY NAME, or reports absence.
func stringByName(m protoreflect.Message, name string) (string, bool) {
	fd := lookupByName(m, name, protoreflect.StringKind)
	if fd == nil {
		return "", false
	}
	return m.Get(fd).String(), true
}

// int32ByName reads an int32 field BY NAME, or reports absence.
func int32ByName(m protoreflect.Message, name string) (int32, bool) {
	fd := lookupByName(m, name, protoreflect.Int32Kind)
	if fd == nil {
		return 0, false
	}
	return int32(m.Get(fd).Int()), true
}

// boolByName reads a bool field BY NAME, or reports absence.
func boolByName(m protoreflect.Message, name string) (bool, bool) {
	fd := lookupByName(m, name, protoreflect.BoolKind)
	if fd == nil {
		return false, false
	}
	return m.Get(fd).Bool(), true
}

// stringListByName reads a repeated string field BY NAME, or reports absence.
func stringListByName(m protoreflect.Message, name string) ([]string, bool) {
	fd := lookupListByName(m, name, protoreflect.StringKind)
	if fd == nil {
		return nil, false
	}
	list := m.Get(fd).List()
	out := make([]string, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		out = append(out, list.Get(i).String())
	}
	return out, true
}

// groupsByName reads the repeated MyAccessGroup field BY NAME. The nested
// message's own fields are read by name for the same reason the outer ones
// are: record A settles `MyAccessGroup`'s field names, not its numbers.
func groupsByName(m protoreflect.Message, name string) ([]Group, bool) {
	fd := lookupListByName(m, name, protoreflect.MessageKind)
	if fd == nil {
		return nil, false
	}
	list := m.Get(fd).List()
	out := make([]Group, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		item := list.Get(i).Message()
		g := Group{}
		g.ID, _ = stringByName(item, fieldID)
		g.Name, _ = stringByName(item, fieldName)
		g.Kind, _ = stringByName(item, fieldKind)
		g.AccountID, _ = stringByName(item, fieldAccountID)
		g.AccountName, _ = stringByName(item, fieldAccountName)
		out = append(out, g)
	}
	return out, true
}

// lookupListByName is lookupByName for a REPEATED field.
func lookupListByName(m protoreflect.Message, name string, kind protoreflect.Kind) protoreflect.FieldDescriptor {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || !fd.IsList() || fd.IsMap() || fd.Kind() != kind {
		return nil
	}
	return fd
}

// lookupByName finds a field by NAME AND KIND, or returns nil.
//
// ===========================================================================
// BY NAME, NEVER BY NUMBER -- AND THAT IS NOT A STYLE CHOICE
// ===========================================================================
// Both design records that add fields to this message settle the NAMES and
// explicitly leave the NUMBERS to whoever writes the engine change:
//
//	"Field numbers are chosen by the implementer against the current
//	 message; the names are the contract."
//	     -- 2026-09-07-groups-and-grants-design.md, section on the wire
//
//	"gains `string role = 11`, `string role_name = 12`, `int32 rank = 13`
//	 (numbers chosen against the message by the implementer ...)"
//	     -- 2026-09-07-roles-as-data-design.md, section G
//
// So a number written down here is a GUESS the engine is free to contradict,
// and the failure would be silent in the worst way: field 11 on the landed
// message might be a different string, and this would render it as the
// operator's role.
//
// It also rules out the obvious alternative. Proto keeps unrecognised fields
// as `unknownFields` bytes, but those bytes are keyed BY NUMBER ONLY -- the
// name never travels on the wire. There is therefore no way to pull `role`
// out of an unknown-field blob without already knowing its number, which is
// the thing the records decline to settle. Reading the DESCRIPTOR by name is
// the only correct option, and it needs no cockpit change when the engine
// lands: the field simply starts existing.
//
// The KIND is checked too, so a future field that reuses one of these names
// for a different type is treated as absent rather than coerced.
func lookupByName(m protoreflect.Message, name string, kind protoreflect.Kind) protoreflect.FieldDescriptor {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.Kind() != kind || fd.IsList() || fd.IsMap() {
		return nil
	}
	return fd
}
