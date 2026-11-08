package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/api/people/v1"
)

func TestCheckClassify(t *testing.T) {
	ix := newDstIndex([]*people.Person{
		{
			Names:        []*people.Name{{DisplayName: "Ana"}},
			PhoneNumbers: []*people.PhoneNumber{{Value: "11 91111-1111"}},
			ClientData:   []*people.ClientData{{Key: clientDataKey, Value: "a@x.com|people/c1"}},
		},
		{
			Names:          []*people.Name{{DisplayName: "Bruno"}},
			EmailAddresses: []*people.EmailAddress{{Value: "bruno@x.com"}},
		},
		{
			Names:        []*people.Name{{DisplayName: "Carla"}},
			PhoneNumbers: []*people.PhoneNumber{{Value: "+55 21 93333-3333"}},
		},
		{Names: []*people.Name{{DisplayName: "Daniel Lima"}}},
	})

	cases := []struct {
		name string
		p    *people.Person
		want checkStatus
		lost []string
	}{
		{"importado", &people.Person{ResourceName: "people/c1", Names: []*people.Name{{DisplayName: "Ana"}}}, checkImported, nil},
		{"importado e editado no destino", &people.Person{
			ResourceName: "people/c1",
			PhoneNumbers: []*people.PhoneNumber{{Value: "11 92222-2222"}},
		}, checkImported, []string{"telefone 11 92222-2222"}},
		{"email já existia", &people.Person{
			ResourceName:   "people/c2",
			EmailAddresses: []*people.EmailAddress{{Value: "Bruno@X.com"}},
			PhoneNumbers:   []*people.PhoneNumber{{Value: "11 94444-4444"}},
			Addresses:      []*people.Address{{City: "Recife"}},
		}, checkEmail, []string{"telefone 11 94444-4444", "endereço"}},
		{"telefone já existia", &people.Person{ResourceName: "people/c3", PhoneNumbers: []*people.PhoneNumber{{Value: "(21) 3333-3333"}}}, checkPhone, nil},
		{"só o nome", &people.Person{ResourceName: "people/c4", Names: []*people.Name{{GivenName: "Daniel", FamilyName: "Lima"}}}, checkName, nil},
		{"sumiu", &people.Person{ResourceName: "people/c5", Names: []*people.Name{{DisplayName: "Eva"}}}, checkMissing, nil},
		{"vazio", &people.Person{}, checkMissing, nil},
	}
	for _, c := range cases {
		r := ix.classify(c.p)
		if r.status != c.want {
			t.Errorf("%s: status = %v, quer %v (%s)", c.name, r.status, c.want, r.reason)
		}
		if !slices.Equal(r.lost, c.lost) {
			t.Errorf("%s: lost = %q, quer %q", c.name, r.lost, c.lost)
		}
	}
}

func TestLoadBackupsDedupes(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	os.WriteFile(a, []byte(`[{"resourceName":"people/c1"},{"resourceName":"people/c2"}]`), 0o600)
	os.WriteFile(b, []byte(`[{"resourceName":"people/c2"},{"resourceName":"people/c3"}]`), 0o600)
	got, err := loadBackups([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("esperava 3 contatos sem repetição, veio %d", len(got))
	}
}
