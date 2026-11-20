// synccontacts copies contacts from one Google account to another without
// creating duplicates.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type options struct {
	credentials string
	tokenDir    string
	dryRun      bool
	matchName   bool
	labels      bool
	yes         bool
	relogin     bool
	verbose     bool
	batchSize   int

	deleteSource     bool
	deleteDuplicates bool

	check bool
}

func main() {
	opts := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	if opts.check {
		err = runCheck(ctx, opts, flag.Args())
	} else {
		err = run(ctx, opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	defDir := ".synccontacts"
	if d, err := os.UserConfigDir(); err == nil {
		defDir = filepath.Join(d, "synccontacts")
	}
	var o options
	flag.StringVar(&o.credentials, "credentials", "credentials.json", "OAuth client file (type \"Desktop app\") downloaded from Google Cloud")
	flag.StringVar(&o.tokenDir, "token-dir", defDir, "directory where the tokens of both accounts are stored")
	flag.BoolVar(&o.dryRun, "dry-run", false, "only show what would be done, without changing anything in the destination")
	flag.BoolVar(&o.matchName, "match-name", true, "treat a contact with the same full name as a duplicate")
	flag.BoolVar(&o.labels, "labels", true, "copy the contacts' labels, creating the missing ones in the destination")
	flag.BoolVar(&o.yes, "yes", false, "do not ask for confirmation before creating the contacts")
	flag.BoolVar(&o.relogin, "relogin", false, "discard the saved tokens and sign in to both accounts again")
	flag.BoolVar(&o.verbose, "v", false, "also list the contacts skipped because they are duplicates")
	flag.IntVar(&o.batchSize, "batch", maxBatchCreate, "how many contacts to create per request (1-200)")
	flag.BoolVar(&o.deleteSource, "delete-source", false, "after copying, DELETE the copied contacts from the source (now or in previous runs)")
	flag.BoolVar(&o.deleteDuplicates, "delete-duplicates", false, "implies -delete-source; also deletes from the source the contacts skipped because their email or phone already existed in the destination")
	flag.BoolVar(&o.check, "check", false, "copy nothing: check in the destination what happened to the contacts in the backups (backup_source_*.json) and why")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n       %s -check [options] [backup_source_*.json ...]\n\n"+
			"Copies the contacts of one Google account (source) to another (destination), without duplicating.\n\n", os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	o.batchSize = max(1, min(o.batchSize, maxBatchCreate))
	if o.deleteDuplicates {
		o.deleteSource = true
	}
	return o
}

func run(ctx context.Context, o options) error {
	if err := os.MkdirAll(o.tokenDir, 0o700); err != nil {
		return err
	}
	srcScope, srcTokName := people.ContactsReadonlyScope, "token_source.json"
	if o.deleteSource {
		// Deleting requires write permission on the source. Uses a separate
		// token so the saved read-only token is not reused.
		srcScope, srcTokName = people.ContactsScope, "token_source_write.json"
	}
	srcTok := filepath.Join(o.tokenDir, srcTokName)
	dstTok := filepath.Join(o.tokenDir, "token_destination.json")
	if o.relogin {
		for _, f := range []string{"token_source.json", "token_source_write.json", "token_destination.json"} {
			os.Remove(filepath.Join(o.tokenDir, f))
		}
	}

	src, err := authenticate(ctx, o.credentials, srcTok, "SOURCE", srcScope)
	if err != nil {
		return err
	}
	dst, err := authenticate(ctx, o.credentials, dstTok, "DESTINATION", people.ContactsScope)
	if err != nil {
		return err
	}
	if strings.EqualFold(src.Email, dst.Email) {
		return fmt.Errorf("source and destination are the same account (%s); run with -relogin and choose different accounts", src.Email)
	}
	fmt.Printf("\nSource:      %s\nDestination: %s\n\n", src.Email, dst.Email)

	srcSvc, err := people.NewService(ctx, option.WithHTTPClient(src.Client))
	if err != nil {
		return err
	}
	dstSvc, err := people.NewService(ctx, option.WithHTTPClient(dst.Client))
	if err != nil {
		return err
	}

	srcContacts, err := listContacts(ctx, srcSvc)
	if err != nil {
		return fmt.Errorf("reading source contacts: %w", err)
	}
	dstContacts, err := listContacts(ctx, dstSvc)
	if err != nil {
		return fmt.Errorf("reading destination contacts: %w", err)
	}
	fmt.Printf("%d contacts in the source, %d in the destination.\n\n", len(srcContacts), len(dstContacts))

	ix := newIndex()
	for _, p := range dstContacts {
		ix.add(p)
	}

	// toDelete: source contacts that will be deleted after the copy.
	var toCreate, toDelete []*people.Person
	for _, p := range srcContacts {
		kind, reason := ix.match(p, sourceID(src.Email, p), o.matchName)
		if kind == noMatch {
			// Added to the index so contacts that are duplicated within the
			// source itself are not created twice.
			ix.add(p)
			toCreate = append(toCreate, p)
			fmt.Printf("  + %s\n", displayName(p))
			continue
		}
		// Contacts that matched only by name are never deleted: they may be
		// different people, or have data the destination contact lacks.
		del := o.deleteSource && (kind == matchImported ||
			(o.deleteDuplicates && (kind == matchEmail || kind == matchPhone)))
		if del {
			toDelete = append(toDelete, p)
			fmt.Printf("  - %s — %s\n", displayName(p), reason)
		} else if o.verbose {
			fmt.Printf("  = %s — %s\n", displayName(p), reason)
		}
	}
	if o.deleteSource {
		toDelete = append(toDelete, toCreate...)
	}

	var groups *groupMapper
	if o.labels {
		if groups, err = newGroupMapper(ctx, srcSvc, dstSvc); err != nil {
			return err
		}
	}
	missingLabels := groups.missing(toCreate)

	fmt.Printf("\nSummary: %d to create, %d already exist in the destination.\n", len(toCreate), len(srcContacts)-len(toCreate))
	if len(missingLabels) > 0 {
		fmt.Printf("Labels to create in the destination: %s\n", strings.Join(missingLabels, ", "))
	}
	if o.deleteSource {
		fmt.Printf("%d contacts will be DELETED from the source (%s) after the copy.\n", len(toDelete), src.Email)
	}
	if len(toCreate) == 0 && len(toDelete) == 0 {
		fmt.Println("Nothing to do.")
		return nil
	}
	if o.dryRun {
		fmt.Println("-dry-run mode: nothing was changed.")
		return nil
	}

	if len(toCreate) > 0 {
		if !o.yes && !confirm(fmt.Sprintf("Create %d contacts in %s?", len(toCreate), dst.Email)) {
			fmt.Println("Cancelled. Nothing was changed.")
			return nil
		}
		if err := groups.create(ctx, dstSvc, missingLabels); err != nil {
			return err
		}
		persons := make([]*people.Person, len(toCreate))
		for i, p := range toCreate {
			persons[i] = copyPerson(p, src.Email, groups)
		}
		created, err := batchCreate(ctx, dstSvc, persons, o.batchSize)
		if err != nil {
			// Nothing is deleted from the source if the copy did not finish.
			return fmt.Errorf("%d of %d contacts created before the failure; nothing was deleted from the source "+
				"(running again resumes where it stopped): %w", created, len(persons), err)
		}
		fmt.Printf("\n%d contacts copied to %s.\n", created, dst.Email)
	}

	if len(toDelete) > 0 {
		fmt.Println()
		if !o.yes && !confirm(fmt.Sprintf("DELETE %d contacts from the source account %s?", len(toDelete), src.Email)) {
			fmt.Println("No contacts were deleted from the source.")
			return nil
		}
		backup, err := writeBackup(toDelete)
		if err != nil {
			return fmt.Errorf("%w; no contacts were deleted", err)
		}
		fmt.Printf("Backup of the contacts about to be deleted: %s\n", backup)
		deleted, err := batchDelete(ctx, srcSvc, toDelete)
		if err != nil {
			return fmt.Errorf("%d of %d contacts deleted from the source before the failure: %w", deleted, len(toDelete), err)
		}
		fmt.Printf("%d contacts deleted from %s.\n", deleted, src.Email)
	}
	fmt.Println("\nDone.")
	return nil
}

func confirm(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
