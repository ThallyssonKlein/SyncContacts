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
	maxBatchCreate = 200 // API limit for people.batchCreateContacts
	maxBatchDelete = 500 // API limit for people.batchDeleteContacts
)

func listContacts(ctx context.Context, svc *people.Service) ([]*people.Person, error) {
	var all []*people.Person
	err := svc.People.Connections.List("people/me").
		PersonFields(personFields).
		// Only the contact's own data, without mixing in the person's public
		// Google profile.
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

// copyPerson builds a new contact, ready to be created in the destination,
// from a source contact. Source metadata (ids, etag, source) and read-only
// fields are dropped.
func copyPerson(src *people.Person, sourceEmail string, groups *groupMapper) *people.Person {
	p := &people.Person{
		// names, birthdays and biographies accept only one value on contacts.
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

// batchCreate creates the contacts in the destination in batches, sequentially
// (Google's recommendation for writes to the same account).
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
		fmt.Printf("  %d/%d contacts created\n", created, len(persons))
	}
	return created, nil
}

// batchDelete deletes contacts (by resourceName) in sequential batches.
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
		fmt.Printf("  %d/%d contacts deleted\n", deleted, len(persons))
	}
	return deleted, nil
}

// writeBackup saves the source contacts that are about to be deleted as JSON,
// so the data can be recovered if something goes wrong.
func writeBackup(persons []*people.Person) (string, error) {
	name := "backup_source_" + time.Now().Format("20060102-150405") + ".json"
	b, err := json.MarshalIndent(persons, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(name, b, 0o600); err != nil {
		return "", fmt.Errorf("saving backup %s: %w", name, err)
	}
	return name, nil
}

// withRetry repeats the call when the API reports that the rate limit was
// reached (429) or that it is temporarily unavailable (503).
func withRetry(ctx context.Context, fn func() error) error {
	delay := 5 * time.Second
	for attempt := 1; ; attempt++ {
		err := fn()
		var gerr *googleapi.Error
		if err == nil || attempt == 6 || !errors.As(err, &gerr) ||
			(gerr.Code != http.StatusTooManyRequests && gerr.Code != http.StatusServiceUnavailable) {
			return err
		}
		fmt.Printf("  API responded HTTP %d, retrying in %s...\n", gerr.Code, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// groupMapper translates the source's labels to the destination's, matching
// them by name.
type groupMapper struct {
	srcNames  map[string]string // resourceName in the source -> name
	dstByName map[string]string // name -> resourceName in the destination
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
		return nil, fmt.Errorf("listing source labels: %w", err)
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
		return nil, fmt.Errorf("listing destination labels: %w", err)
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

// missing lists the labels used by contacts that do not exist in the
// destination yet.
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
			return fmt.Errorf("creating label %q: %w", name, err)
		}
		g.dstByName[name] = cg.ResourceName
		fmt.Printf("  label created: %s\n", name)
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
	// When labels are given at creation, "My Contacts" is included explicitly
	// so the contact does not end up outside the main list.
	return append(out, &people.Membership{
		ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: "contactGroups/myContacts"},
	})
}
