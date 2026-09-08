package access

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// FuzzDecodeMatchesFieldNamesExactly asserts the property the whole seam rests
// on: a field populates a Summary slot ONLY when its name is character-for-
// character one of the contract names AND its kind and cardinality match.
//
// The failure this catches is not a panic -- nothing here panics. It is the
// helpful-looking change: matching case-insensitively, trimming, accepting
// `clusterRole` as a synonym for `role`, or coercing a string into `rank`.
// Any of those would make the cockpit render a value the cluster never claimed
// as the operator's role, and it would look completely plausible on screen.
// The engine is free to add fields this cockpit has never heard of, so "some
// other field was present" must stay indistinguishable from "no field".
//
// A single STRING field is the probe because it can satisfy at most two of the
// six contract slots; the other four are an int32, a bool, a repeated string
// and a repeated message, so a string must leave every one of them absent.
func FuzzDecodeMatchesFieldNamesExactly(f *testing.F) {
	f.Add("role", "owner")
	f.Add("role_name", "Owner")
	f.Add("Role", "owner")
	f.Add("ROLE", "owner")
	f.Add("role_", "owner")
	f.Add("_role", "owner")
	f.Add("cluster_role", "owner")
	f.Add("clusterRole", "owner")
	f.Add("rank", "150")
	f.Add("account_ids", "acct_1")
	f.Add("every_account", "true")
	f.Add("groups", "g_1")
	f.Add("request_id", "")

	f.Fuzz(func(t *testing.T, name, value string) {
		m, ok := singleStringField(name, value)
		if !ok {
			t.Skip("not a valid proto3 field name")
		}

		var got Summary
		decodePending(m, &got)

		if want := name == "role"; got.Role.Reported != want {
			t.Fatalf("field %q: Role.Reported = %v, want %v", name, got.Role.Reported, want)
		}
		if want := ""; name == "role" {
			want = value
			if got.Role.Slug != want {
				t.Fatalf("field %q: Role.Slug = %q, want %q", name, got.Role.Slug, want)
			}
		} else if got.Role.Slug != want {
			t.Fatalf("field %q: Role.Slug = %q, want empty", name, got.Role.Slug)
		}
		if name == "role_name" {
			if got.Role.Name != value {
				t.Fatalf("field %q: Role.Name = %q, want %q", name, got.Role.Name, value)
			}
		} else if got.Role.Name != "" {
			t.Fatalf("field %q: Role.Name = %q, want empty", name, got.Role.Name)
		}

		// A string can satisfy none of these, whatever it is called.
		if got.Role.HasRank {
			t.Fatalf("field %q (string): Role.HasRank = true; `rank` is an int32", name)
		}
		if got.Groups.Reported {
			t.Fatalf("field %q (string): Groups.Reported = true; `groups` is a repeated message", name)
		}
		if got.Scope.Reported {
			t.Fatalf("field %q (string): Scope.Reported = true; the scope fields are a repeated string and a bool", name)
		}
	})
}

// singleStringField builds a message carrying exactly one string field with
// the given name. Reports false when proto itself will not accept the name.
func singleStringField(name, value string) (protoreflect.Message, bool) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("cockpit_fuzz_one_field.proto"),
		Package: proto.String("cockpit.fuzz"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:  proto.String("Probe"),
			Field: []*descriptorpb.FieldDescriptorProto{strField(name, 1)},
		}},
	}
	fd, err := protodesc.NewFile(file, nil)
	if err != nil {
		return nil, false
	}
	md := fd.Messages().ByName("Probe")
	if md == nil || md.Fields().Len() != 1 {
		return nil, false
	}
	m := dynamicpb.NewMessage(md)
	m.Set(md.Fields().Get(0), protoreflect.ValueOfString(value))
	return m, true
}
