package main

// -check mode: checks in the destination account what happened to each contact
// saved in the backups (backup_source_*.json), i.e. the contacts that were
// deleted from the source. Changes nothing in either account.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type checkStatus int

const (
	checkImported checkStatus = iota // created in the destination by this program
	checkEmail                       // not created: the email already existed in the destination
	checkPhone                       // not created: the phone already existed in the destination
	checkName                        // the destination only has someone with the same name
	checkMissing                     // nothing in the destination matches it
)

type checkResult struct {
	src    *people.Person
	status checkStatus
	dst    *people.Person // matching contact in the destination (nil for checkMissing)
	reason string
	lost   []string // data from the source contact that is not in the destination one
}

// dstIndex indexes the destination contacts keeping the contact itself, so its
// data can later be compared with the source's.
type dstIndex struct {
	bySourceRN map[string]*people.Person // resourceName in the source -> created contact
	emails     map[string]*people.Person
	phones     map[string]*people.Person
	names      map[string]*people.Person
}

func newDstIndex(contacts []*people.Person) *dstIndex {
	ix := &dstIndex{
		bySourceRN: map[string]*people.Person{},
		emails:     map[string]*people.Person{},
		phones:     map[string]*people.Person{},
		names:      map[string]*people.Person{},
	}
	put := func(m map[string]*people.Person, k string, p *people.Person) {
		if _, ok := m[k]; k != "" && !ok {
			m[k] = p
		}
	}
	for _, p := range contacts {
		for _, cd := range p.ClientData {
			// The value is "<source email>|<resourceName in the source>". The
			// backup does not store the source email, so lookup is by resourceName.
			if cd.Key != clientDataKey {
				continue
			}
			if _, rn, ok := strings.Cut(cd.Value, "|"); ok {
				put(ix.bySourceRN, rn, p)
			}
		}
		for _, e := range p.EmailAddresses {
			put(ix.emails, emailKey(e.Value), p)
		}
		for _, ph := range p.PhoneNumbers {
			put(ix.phones, phoneKey(ph), p)
		}
		put(ix.names, nameKey(p), p)
	}
	return ix
}

func (ix *dstIndex) classify(p *people.Person) checkResult {
	r := checkResult{src: p, status: checkMissing}
	if d, ok := ix.bySourceRN[p.ResourceName]; ok && p.ResourceName != "" {
		r.status, r.dst = checkImported, d
		r.reason = fmt.Sprintf("copied by the program as %q", displayName(d))
	}
	if r.dst == nil {
		for _, e := range p.EmailAddresses {
			if d, ok := ix.emails[emailKey(e.Value)]; ok {
				r.status, r.dst = checkEmail, d
				r.reason = fmt.Sprintf("not created: email %s was already in %q", e.Value, displayName(d))
				break
			}
		}
	}
	if r.dst == nil {
		for _, ph := range p.PhoneNumbers {
			if d, ok := ix.phones[phoneKey(ph)]; ok && phoneKey(ph) != "" {
				r.status, r.dst = checkPhone, d
				r.reason = fmt.Sprintf("not created: phone %s was already in %q", ph.Value, displayName(d))
				break
			}
		}
	}
	if r.dst == nil {
		if d, ok := ix.names[nameKey(p)]; ok && nameKey(p) != "" {
			r.status, r.dst = checkName, d
			r.reason = fmt.Sprintf("only %q exists, with the same name; may be a different person", displayName(d))
		}
	}
	if r.dst == nil {
		r.reason = "no contact in the destination has the program's mark, or the same email, phone or name"
		return r
	}
	r.lost = lostData(p, r.dst)
	return r
}

// lostData lists what the source contact has and the destination one lacks.
func lostData(src, dst *people.Person) []string {
	var out []string
	emails := map[string]bool{}
	for _, e := range dst.EmailAddresses {
		emails[emailKey(e.Value)] = true
	}
	for _, e := range src.EmailAddresses {
		if k := emailKey(e.Value); k != "" && !emails[k] {
			out = append(out, "email "+e.Value)
		}
	}
	phones := map[string]bool{}
	for _, ph := range dst.PhoneNumbers {
		phones[phoneKey(ph)] = true
	}
	for _, ph := range src.PhoneNumbers {
		if k := phoneKey(ph); k != "" && !phones[k] {
			out = append(out, "phone "+ph.Value)
		}
	}
	fields := []struct {
		name     string
		src, dst int
	}{
		{"address", len(src.Addresses), len(dst.Addresses)},
		{"company", len(src.Organizations), len(dst.Organizations)},
		{"birthday", len(src.Birthdays), len(dst.Birthdays)},
		{"notes", len(src.Biographies), len(dst.Biographies)},
		{"nickname", len(src.Nicknames), len(dst.Nicknames)},
		{"website", len(src.Urls), len(dst.Urls)},
		{"dates", len(src.Events), len(dst.Events)},
		{"relatives", len(src.Relations), len(dst.Relations)},
		{"custom fields", len(src.UserDefined), len(dst.UserDefined)},
	}
	for _, f := range fields {
		if f.src > 0 && f.dst == 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// loadBackups reads the backup files, skipping contacts repeated across them.
func loadBackups(paths []string) ([]*people.Person, error) {
	seen := map[string]bool{}
	var out []*people.Person
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var persons []*people.Person
		if err := json.Unmarshal(b, &persons); err != nil {
			return nil, fmt.Errorf("reading backup %s: %w", path, err)
		}
		for _, p := range persons {
			if p.ResourceName != "" {
				if seen[p.ResourceName] {
					continue
				}
				seen[p.ResourceName] = true
			}
			out = append(out, p)
		}
	}
	return out, nil
}

func runCheck(ctx context.Context, o options, files []string) error {
	if len(files) == 0 {
		files, _ = filepath.Glob("backup_source_*.json")
		if len(files) == 0 {
			return fmt.Errorf("no backup_source_*.json in the current directory; pass the files: -check FILE...")
		}
	}
	backup, err := loadBackups(files)
	if err != nil {
		return err
	}
	fmt.Printf("%d contacts in %d backup(s): %s\n", len(backup), len(files), strings.Join(files, ", "))

	if err := os.MkdirAll(o.tokenDir, 0o700); err != nil {
		return err
	}
	dstTok := filepath.Join(o.tokenDir, "token_destination.json")
	if o.relogin {
		os.Remove(dstTok)
	}
	dst, err := authenticate(ctx, o.credentials, dstTok, "DESTINATION", people.ContactsScope)
	if err != nil {
		return err
	}
	svc, err := people.NewService(ctx, option.WithHTTPClient(dst.Client))
	if err != nil {
		return err
	}
	contacts, err := listContacts(ctx, svc)
	if err != nil {
		return fmt.Errorf("reading destination contacts: %w", err)
	}
	fmt.Printf("Destination: %s (%d contacts)\n", dst.Email, len(contacts))

	ix := newDstIndex(contacts)
	byStatus := map[checkStatus][]checkResult{}
	var withLoss []checkResult
	for _, p := range backup {
		r := ix.classify(p)
		byStatus[r.status] = append(byStatus[r.status], r)
		if r.status != checkName && len(r.lost) > 0 {
			withLoss = append(withLoss, r)
		}
	}

	fmt.Printf("\nSummary:\n")
	fmt.Printf("  %4d copied by the program\n", len(byStatus[checkImported]))
	fmt.Printf("  %4d were not created because the email already existed in the destination\n", len(byStatus[checkEmail]))
	fmt.Printf("  %4d were not created because the phone already existed in the destination\n", len(byStatus[checkPhone]))
	fmt.Printf("  %4d only have someone with the same name in the destination\n", len(byStatus[checkName]))
	fmt.Printf("  %4d are NOT in the destination\n", len(byStatus[checkMissing]))
	fmt.Printf("  %4d are in the destination, but missing some data\n", len(withLoss))

	printSection("NOT in the destination", byStatus[checkMissing], true)
	printSection("Matched only by name (check whether it is the same person)", byStatus[checkName], true)
	printSection("In the destination, but missing data", withLoss, false)
	if o.verbose {
		printSection("Copied by the program", byStatus[checkImported], false)
		printSection("Already existed in the destination (email)", byStatus[checkEmail], false)
		printSection("Already existed in the destination (phone)", byStatus[checkPhone], false)
	}

	if n := len(byStatus[checkMissing]); n > 0 {
		fmt.Printf("\nThe %d contacts that are not in the destination were probably deleted or\n"+
			"edited there after the copy (see the Google Contacts Trash). Their complete\n"+
			"data is in the backup files.\n", n)
	}
	if len(byStatus[checkMissing])+len(byStatus[checkName])+len(withLoss) == 0 {
		fmt.Println("\nAll good: every contact in the backup is in the destination, with no data loss.")
	}
	return nil
}

func printSection(title string, rs []checkResult, showContact bool) {
	if len(rs) == 0 {
		return
	}
	rs = append([]checkResult(nil), rs...)
	sort.SliceStable(rs, func(i, j int) bool { return fold(displayName(rs[i].src)) < fold(displayName(rs[j].src)) })
	fmt.Printf("\n%s (%d):\n", title, len(rs))
	for _, r := range rs {
		fmt.Printf("  - %s — %s\n", displayName(r.src), r.reason)
		if showContact {
			if s := contactSummary(r.src); s != "" {
				fmt.Printf("      in the source: %s\n", s)
			}
		}
		if len(r.lost) > 0 {
			fmt.Printf("      missing in the destination: %s\n", strings.Join(r.lost, ", "))
		}
	}
}

// contactSummary shows the contact's emails and phones, to help find it.
func contactSummary(p *people.Person) string {
	var parts []string
	for _, e := range p.EmailAddresses {
		parts = append(parts, e.Value)
	}
	for _, ph := range p.PhoneNumbers {
		parts = append(parts, ph.Value)
	}
	return strings.Join(parts, ", ")
}
