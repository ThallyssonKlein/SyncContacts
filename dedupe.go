package main

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"google.golang.org/api/people/v1"
)

// clientDataKey marks, on each contact created in the destination, which source
// contact it came from. This way running the program again never re-imports the
// same contact, even if someone edited its email/phone in the destination.
const clientDataKey = "syncContactsSourceId"

// Phones are compared by their last 8 digits. This ignores country code, area
// code, carrier zeros and the extra "9" of Brazilian mobile numbers, so
// "+55 (11) 98765-4321", "011 98765 4321" and "8765-4321" match each other.
const phoneSuffixLen = 8

// index holds the comparison keys of the contacts that already exist in the
// destination (and of those already chosen for creation in this run).
type index struct {
	emails    map[string]string // key -> name of the existing contact
	phones    map[string]string
	names     map[string]string
	sourceIDs map[string]string
}

func newIndex() *index {
	return &index{
		emails:    map[string]string{},
		phones:    map[string]string{},
		names:     map[string]string{},
		sourceIDs: map[string]string{},
	}
}

func (ix *index) add(p *people.Person) {
	label := displayName(p)
	for _, e := range p.EmailAddresses {
		if k := emailKey(e.Value); k != "" {
			ix.emails[k] = label
		}
	}
	for _, ph := range p.PhoneNumbers {
		if k := phoneKey(ph); k != "" {
			ix.phones[k] = label
		}
	}
	if k := nameKey(p); k != "" {
		ix.names[k] = label
	}
	for _, cd := range p.ClientData {
		if cd.Key == clientDataKey && cd.Value != "" {
			ix.sourceIDs[cd.Value] = label
		}
	}
}

// matchKind says why a contact was considered a duplicate.
type matchKind int

const (
	noMatch       matchKind = iota
	matchImported           // copied by this program in a previous run
	matchEmail
	matchPhone
	matchName
)

// match says whether p already exists in the index and, if so, why.
func (ix *index) match(p *people.Person, sourceID string, useName bool) (matchKind, string) {
	if l, ok := ix.sourceIDs[sourceID]; ok && sourceID != "" {
		return matchImported, fmt.Sprintf("already imported before as %q", l)
	}
	for _, e := range p.EmailAddresses {
		if l, ok := ix.emails[emailKey(e.Value)]; ok {
			return matchEmail, fmt.Sprintf("email %s is already in %q", e.Value, l)
		}
	}
	for _, ph := range p.PhoneNumbers {
		if l, ok := ix.phones[phoneKey(ph)]; ok {
			return matchPhone, fmt.Sprintf("phone %s is already in %q", ph.Value, l)
		}
	}
	if useName {
		if l, ok := ix.names[nameKey(p)]; ok {
			return matchName, fmt.Sprintf("same name as %q", l)
		}
	}
	return noMatch, ""
}

func emailKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func phoneKey(ph *people.PhoneNumber) string {
	d := digits(ph.Value)
	if d == "" {
		d = digits(ph.CanonicalForm)
	}
	if len(d) > phoneSuffixLen {
		d = d[len(d)-phoneSuffixLen:]
	}
	return d
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameKey normalizes the full name: accent-free, lowercase and with single
// spaces, so "José  da Silva" and "jose da silva" are equal.
func nameKey(p *people.Person) string {
	if len(p.Names) == 0 {
		return ""
	}
	n := p.Names[0]
	full := strings.Join([]string{n.GivenName, n.MiddleName, n.FamilyName}, " ")
	if strings.TrimSpace(full) == "" {
		full = n.DisplayName
	}
	if strings.TrimSpace(full) == "" {
		full = n.UnstructuredName
	}
	return fold(full)
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// displayName returns something readable to identify the contact in the logs.
func displayName(p *people.Person) string {
	if len(p.Names) > 0 {
		n := p.Names[0]
		for _, s := range []string{n.DisplayName, n.UnstructuredName, strings.TrimSpace(n.GivenName + " " + n.FamilyName)} {
			if s != "" {
				return s
			}
		}
	}
	if len(p.EmailAddresses) > 0 {
		return p.EmailAddresses[0].Value
	}
	if len(p.PhoneNumbers) > 0 {
		return p.PhoneNumbers[0].Value
	}
	if len(p.Organizations) > 0 && p.Organizations[0].Name != "" {
		return p.Organizations[0].Name
	}
	return "(no name)"
}
