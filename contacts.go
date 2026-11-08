package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/people/v1"
)

const personFields = "names,nicknames,emailAddresses,phoneNumbers,addresses,organizations," +
	"birthdays,biographies,urls,relations,events,userDefined,imClients,occupations," +
	"memberships,clientData"

const (
	maxBatchCreate = 200 // limite da API para people.batchCreateContacts
	maxBatchDelete = 500 // limite da API para people.batchDeleteContacts
)

func listContacts(ctx context.Context, svc *people.Service) ([]*people.Person, error) {
	var all []*people.Person
	err := svc.People.Connections.List("people/me").
		PersonFields(personFields).
		// Só os dados do contato em si, sem misturar com o perfil público
		// da pessoa no Google.
		Sources("READ_SOURCE_TYPE_CONTACT").
		PageSize(1000).
		Pages(ctx, func(r *people.ListConnectionsResponse) error {
			all = append(all, r.Connections...)
			return nil
		})
	return all, err
}

func sourceID(sourceEmail string, p *people.Person) string {
	return sourceEmail + "|" + p.ResourceName
}

// copyPerson monta um contato novo, pronto para ser criado no destino, a
// partir de um contato da origem. Os metadados da origem (ids, etag, fonte)
// e campos somente-leitura são descartados.
func copyPerson(src *people.Person, sourceEmail string, groups *groupMapper) *people.Person {
	p := &people.Person{
		// names, birthdays e biographies só aceitam um valor em contatos.
		Names: first(cloneAll(src.Names, func(n *people.Name) {
			n.Metadata, n.DisplayName, n.DisplayNameLastFirst = nil, "", ""
		})),
		Birthdays:   first(cloneAll(src.Birthdays, func(b *people.Birthday) { b.Metadata = nil })),
		Biographies: first(cloneAll(src.Biographies, func(b *people.Biography) { b.Metadata = nil })),

		Nicknames: cloneAll(src.Nicknames, func(n *people.Nickname) { n.Metadata = nil }),
		EmailAddresses: cloneAll(src.EmailAddresses, func(e *people.EmailAddress) {
			e.Metadata, e.FormattedType = nil, ""
		}),
		PhoneNumbers: cloneAll(src.PhoneNumbers, func(ph *people.PhoneNumber) {
			ph.Metadata, ph.FormattedType, ph.CanonicalForm = nil, "", ""
		}),
		Addresses: cloneAll(src.Addresses, func(a *people.Address) {
			a.Metadata, a.FormattedType = nil, ""
		}),
		Organizations: cloneAll(src.Organizations, func(o *people.Organization) {
			o.Metadata, o.FormattedType = nil, ""
		}),
		Urls:      cloneAll(src.Urls, func(u *people.Url) { u.Metadata, u.FormattedType = nil, "" }),
		Relations: cloneAll(src.Relations, func(r *people.Relation) { r.Metadata, r.FormattedType = nil, "" }),
		Events:    cloneAll(src.Events, func(e *people.Event) { e.Metadata, e.FormattedType = nil, "" }),
		ImClients: cloneAll(src.ImClients, func(im *people.ImClient) {
			im.Metadata, im.FormattedType, im.FormattedProtocol = nil, "", ""
		}),
		UserDefined: cloneAll(src.UserDefined, func(u *people.UserDefined) { u.Metadata = nil }),
		Occupations: cloneAll(src.Occupations, func(o *people.Occupation) { o.Metadata = nil }),

		ClientData:  []*people.ClientData{{Key: clientDataKey, Value: sourceID(sourceEmail, src)}},
		Memberships: groups.memberships(src),
	}
	return p
}

func cloneAll[T any](in []*T, clear func(*T)) []*T {
	if len(in) == 0 {
		return nil
	}
	out := make([]*T, 0, len(in))
	for _, x := range in {
		c := *x
		clear(&c)
		out = append(out, &c)
	}
	return out
}

func first[T any](in []*T) []*T {
	if len(in) > 1 {
		return in[:1]
	}
	return in
}

// batchCreate cria os contatos no destino em lotes, de forma sequencial
// (recomendação do Google para escritas na mesma conta).
func batchCreate(ctx context.Context, svc *people.Service, persons []*people.Person, batchSize int) (int, error) {
	created := 0
	for start := 0; start < len(persons); start += batchSize {
		end := min(start+batchSize, len(persons))
		req := &people.BatchCreateContactsRequest{ReadMask: "names"}
		for _, p := range persons[start:end] {
			req.Contacts = append(req.Contacts, &people.ContactToCreate{ContactPerson: p})
		}
		err := withRetry(ctx, func() error {
			_, err := svc.People.BatchCreateContacts(req).Context(ctx).Do()
			return err
		})
		if err != nil {
			return created, err
		}
		created = end
		fmt.Printf("  %d/%d contatos criados\n", created, len(persons))
	}
	return created, nil
}

// batchDelete apaga contatos (pelo resourceName) em lotes sequenciais.
func batchDelete(ctx context.Context, svc *people.Service, persons []*people.Person) (int, error) {
	deleted := 0
	for start := 0; start < len(persons); start += maxBatchDelete {
		end := min(start+maxBatchDelete, len(persons))
		req := &people.BatchDeleteContactsRequest{}
		for _, p := range persons[start:end] {
			req.ResourceNames = append(req.ResourceNames, p.ResourceName)
		}
		err := withRetry(ctx, func() error {
			_, err := svc.People.BatchDeleteContacts(req).Context(ctx).Do()
			return err
		})
		if err != nil {
			return deleted, err
		}
		deleted = end
		fmt.Printf("  %d/%d contatos apagados\n", deleted, len(persons))
	}
	return deleted, nil
}

// writeBackup salva em JSON os contatos da origem que serão apagados, para
// ter como recuperar os dados se algo der errado.
func writeBackup(persons []*people.Person) (string, error) {
	name := "backup_origem_" + time.Now().Format("20060102-150405") + ".json"
	b, err := json.MarshalIndent(persons, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(name, b, 0o600); err != nil {
		return "", fmt.Errorf("salvando backup %s: %w", name, err)
	}
	return name, nil
}

// withRetry repete a chamada quando a API responde que o limite de uso foi
// atingido (429) ou que está temporariamente indisponível (503).
func withRetry(ctx context.Context, fn func() error) error {
	delay := 5 * time.Second
	for attempt := 1; ; attempt++ {
		err := fn()
		var gerr *googleapi.Error
		if err == nil || attempt == 6 || !errors.As(err, &gerr) ||
			(gerr.Code != http.StatusTooManyRequests && gerr.Code != http.StatusServiceUnavailable) {
			return err
		}
		fmt.Printf("  API respondeu HTTP %d, tentando de novo em %s...\n", gerr.Code, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// groupMapper traduz os marcadores (labels) da origem para os do destino,
// casando pelo nome.
type groupMapper struct {
	srcNames  map[string]string // resourceName na origem -> nome
	dstByName map[string]string // nome -> resourceName no destino
}

func newGroupMapper(ctx context.Context, srcSvc, dstSvc *people.Service) (*groupMapper, error) {
	g := &groupMapper{srcNames: map[string]string{}, dstByName: map[string]string{}}
	err := srcSvc.ContactGroups.List().GroupFields("name,groupType").PageSize(1000).
		Pages(ctx, func(r *people.ListContactGroupsResponse) error {
			for _, cg := range r.ContactGroups {
				if cg.GroupType == "USER_CONTACT_GROUP" {
					g.srcNames[cg.ResourceName] = cg.Name
				}
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listando marcadores da origem: %w", err)
	}
	err = dstSvc.ContactGroups.List().GroupFields("name,groupType").PageSize(1000).
		Pages(ctx, func(r *people.ListContactGroupsResponse) error {
			for _, cg := range r.ContactGroups {
				if cg.GroupType == "USER_CONTACT_GROUP" {
					g.dstByName[cg.Name] = cg.ResourceName
				}
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listando marcadores do destino: %w", err)
	}
	return g, nil
}

func (g *groupMapper) labelsOf(p *people.Person) []string {
	var out []string
	for _, m := range p.Memberships {
		if m.ContactGroupMembership == nil {
			continue
		}
		if name, ok := g.srcNames[m.ContactGroupMembership.ContactGroupResourceName]; ok {
			out = append(out, name)
		}
	}
	return out
}

// missing lista os marcadores usados pelos contatos que ainda não existem no destino.
func (g *groupMapper) missing(persons []*people.Person) []string {
	if g == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range persons {
		for _, name := range g.labelsOf(p) {
			if _, ok := g.dstByName[name]; !ok && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (g *groupMapper) create(ctx context.Context, svc *people.Service, names []string) error {
	for _, name := range names {
		var cg *people.ContactGroup
		err := withRetry(ctx, func() error {
			var err error
			cg, err = svc.ContactGroups.Create(&people.CreateContactGroupRequest{
				ContactGroup: &people.ContactGroup{Name: name},
			}).Context(ctx).Do()
			return err
		})
		if err != nil {
			return fmt.Errorf("criando marcador %q: %w", name, err)
		}
		g.dstByName[name] = cg.ResourceName
		fmt.Printf("  marcador criado: %s\n", name)
	}
	return nil
}

func (g *groupMapper) memberships(p *people.Person) []*people.Membership {
	if g == nil {
		return nil
	}
	var out []*people.Membership
	for _, name := range g.labelsOf(p) {
		if rn, ok := g.dstByName[name]; ok {
			out = append(out, &people.Membership{
				ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: rn},
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	// Ao informar marcadores na criação, inclui "Meus contatos" explicitamente
	// para o contato não ficar fora da lista principal.
	return append(out, &people.Membership{
		ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: "contactGroups/myContacts"},
	})
}
