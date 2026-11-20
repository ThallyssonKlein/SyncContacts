package main

import (
	"testing"

	"google.golang.org/api/people/v1"
)

func TestPhoneKey(t *testing.T) {
	same := []string{"+55 (11) 98765-4321", "011 98765 4321", "11987654321", "98765-4321", "8765-4321"}
	want := phoneKey(&people.PhoneNumber{Value: same[0]})
	for _, v := range same[1:] {
		if got := phoneKey(&people.PhoneNumber{Value: v}); got != want {
			t.Errorf("phoneKey(%q) = %q, want %q", v, got, want)
		}
	}
	if phoneKey(&people.PhoneNumber{Value: "+55 11 91234-5678"}) == want {
		t.Error("different phones should not match")
	}
	if got := phoneKey(&people.PhoneNumber{CanonicalForm: "+5511987654321"}); got != want {
		t.Errorf("should use canonicalForm when value is empty, got %q", got)
	}
}

func TestNameKey(t *testing.T) {
	a := &people.Person{Names: []*people.Name{{GivenName: "José", FamilyName: "da  Conceição"}}}
	b := &people.Person{Names: []*people.Name{{DisplayName: "jose DA conceicao"}}}
	if nameKey(a) != nameKey(b) {
		t.Errorf("nameKey: %q != %q", nameKey(a), nameKey(b))
	}
	if nameKey(&people.Person{}) != "" {
		t.Error("a contact without a name should have an empty key")
	}
}

func TestIndexMatch(t *testing.T) {
	ix := newIndex()
	ix.add(&people.Person{
		Names:          []*people.Name{{DisplayName: "Maria Souza"}},
		EmailAddresses: []*people.EmailAddress{{Value: "Maria@Example.com"}},
		PhoneNumbers:   []*people.PhoneNumber{{Value: "(21) 99999-0000"}},
		ClientData:     []*people.ClientData{{Key: clientDataKey, Value: "a@x.com|people/c1"}},
	})

	cases := []struct {
		name    string
		p       *people.Person
		srcID   string
		useName bool
		want    matchKind
	}{
		{"same email", &people.Person{EmailAddresses: []*people.EmailAddress{{Value: " maria@example.com "}}}, "", true, matchEmail},
		{"same phone", &people.Person{PhoneNumbers: []*people.PhoneNumber{{Value: "+55 21 999990000"}}}, "", true, matchPhone},
		{"same name", &people.Person{Names: []*people.Name{{GivenName: "Maria", FamilyName: "Souza"}}}, "", true, matchName},
		{"same name without -match-name", &people.Person{Names: []*people.Name{{GivenName: "Maria", FamilyName: "Souza"}}}, "", false, noMatch},
		{"already imported", &people.Person{EmailAddresses: []*people.EmailAddress{{Value: "maria@example.com"}}}, "a@x.com|people/c1", true, matchImported},
		{"new", &people.Person{
			Names:        []*people.Name{{DisplayName: "João"}},
			PhoneNumbers: []*people.PhoneNumber{{Value: "11 3333-4444"}},
		}, "a@x.com|people/c2", true, noMatch},
		{"empty", &people.Person{}, "", true, noMatch},
	}
	for _, c := range cases {
		if got, _ := ix.match(c.p, c.srcID, c.useName); got != c.want {
			t.Errorf("%s: match = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCopyPersonStripsSourceMetadata(t *testing.T) {
	src := &people.Person{
		ResourceName: "people/c1",
		Etag:         "etag",
		Names: []*people.Name{
			{GivenName: "Ana", DisplayName: "Ana", Metadata: &people.FieldMetadata{Primary: true}},
			{GivenName: "Other"},
		},
		PhoneNumbers: []*people.PhoneNumber{{Value: "123", CanonicalForm: "+123", Metadata: &people.FieldMetadata{}}},
	}
	p := copyPerson(src, "a@x.com", nil)
	if p.ResourceName != "" || p.Etag != "" {
		t.Error("the source's resourceName/etag must not go to the destination")
	}
	if len(p.Names) != 1 || p.Names[0].Metadata != nil || p.Names[0].DisplayName != "" {
		t.Errorf("names badly copied: %+v", p.Names)
	}
	if p.PhoneNumbers[0].Metadata != nil || p.PhoneNumbers[0].CanonicalForm != "" || p.PhoneNumbers[0].Value != "123" {
		t.Errorf("phone badly copied: %+v", p.PhoneNumbers[0])
	}
	if src.Names[0].Metadata == nil {
		t.Error("copyPerson must not modify the source contact")
	}
	if len(p.ClientData) != 1 || p.ClientData[0].Value != "a@x.com|people/c1" {
		t.Errorf("clientData = %+v", p.ClientData)
	}
}
