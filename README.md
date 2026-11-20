# SyncContacts

Copies Google Contacts from one Google account (**source**) to another
(**destination**) without creating duplicates. The source account is accessed
read-only, unless you ask to delete what was copied from it
(`-delete-source`).

## How it avoids duplicates

A source contact is **not** created in the destination when:

1. it was already imported by this program before (each created contact stores
   the source contact's id in `clientData`). So you can run it as many times as
   you like.
2. any of its **emails** already exists in the destination (case-insensitive);
3. any of its **phones** already exists in the destination. The comparison uses
   the last 8 digits, so `+55 (11) 98765-4321`, `011 98765 4321` and
   `8765-4321` are treated as the same number;
4. the **full name** is the same (ignoring accents and case). To turn this rule
   off, use `-match-name=false`.

Contacts repeated within the source itself are also created only once.

## What is copied

Name, nicknames, emails, phones, addresses, company/job title, birthday, notes,
websites, relatives, dates, custom fields, messengers, occupation and labels.
Labels that do not exist in the destination are created (`-labels=false` turns
this off).

**Not** copied: photos, and "Other contacts" (the ones Gmail saves
automatically).

## Setup (one time only)

1. Go to the [Google Cloud Console](https://console.cloud.google.com/) and
   create a project (or use an existing one).
2. Under **APIs & Services → Library**, enable the **Google People API**.
3. Under **APIs & Services → OAuth consent screen** (Google Auth Platform):
   - user type **External**;
   - under **Audience → Test users**, add **both accounts** (source and
     destination).
4. Under **Credentials → Create credentials → OAuth client ID**, choose the
   **Desktop app** type and download the JSON.
5. Save the file as `credentials.json` in the project folder.

> While the app is in "Testing" mode, Google expires tokens after 7 days. If
> you get an authentication error after that, the program asks you to sign in
> again by itself.

## Usage

```sh
go build -o synccontacts .

# 1) Simulation: shows what would be created, without changing anything
./synccontacts -dry-run

# 2) Real copy (asks for confirmation first)
./synccontacts
```

On the first run the browser opens twice: first sign in to the **SOURCE**
account, then to the **DESTINATION** one. Tokens are stored in
`~/.config/synccontacts/` and later runs do not ask for a login. The program
shows both accounts' emails before any change and refuses to run if they are
the same.

| Option | Description |
|---|---|
| `-dry-run` | only shows what would be done |
| `-v` | also lists the skipped contacts and why |
| `-match-name=false` | do not treat having the same name as a duplicate |
| `-labels=false` | do not copy labels |
| `-yes` | do not ask for confirmation |
| `-relogin` | forget the saved tokens and sign in again (to switch accounts) |
| `-credentials FILE` | path to the OAuth client JSON (default `credentials.json`) |
| `-token-dir DIR` | where to store the tokens |
| `-batch N` | contacts per request (1–200, default 200) |
| `-delete-source` | after copying, deletes the copied contacts from the source |
| `-delete-duplicates` | also deletes from the source the ones that already existed in the destination (by email/phone) |

If the copy fails midway (network drop, API limit), just run it again: the
contacts that were already created are recognized and skipped.

## Deleting from the source what was copied (move instead of copy)

```sh
./synccontacts -delete-source -dry-run   # check the list marked with "-"
./synccontacts -delete-source
```

With `-delete-source`, after the copy the program deletes from the source:

- the contacts created in the destination in this run;
- the contacts it had already copied in previous runs.

With `-delete-duplicates`, it also deletes the contacts that were skipped
because their **email or phone** already existed in the destination. Be
careful: the destination contact may not have all the data of the source
contact.

Safeguards:

- contacts that matched only by **name** are never deleted;
- if the copy fails midway, **nothing** is deleted;
- it asks for a separate confirmation before deleting (unless `-yes`);
- before deleting, it saves the contacts to `backup_source_<date>.json` in the
  current directory;
- Google Contacts also lets you undo changes from the last 30 days under
  **Settings → Undo changes**, in the source account.

Deleting requires write permission on the source account. So, the first time
you use `-delete-source`, the source login is requested again, this time asking
for edit access.

## Checking what ended up in the destination

```sh
./synccontacts -check                     # uses the backup_source_*.json files in the current directory
./synccontacts -check -v backup_source_20261108-105808.json
```

Reads the contacts from the backups (the ones deleted from the source), lists
the destination's contacts and shows what happened to each one:

- **copied by the program**: the destination contact carries its source mark;
- **email/phone already existed**: it was not created because a contact with
  that email or phone already existed in the destination;
- **matched only by name**: the destination only has someone with the same
  name;
- **not in the destination**: nothing matches it (probably deleted or edited in
  the destination after the copy). The data is in the backup.

It also warns when the destination contact lacks some data the source one had
(another phone, email, address, birthday, notes...). By default it lists only
the problem cases; with `-v`, it lists all of them. It only signs in to the
destination and changes nothing. Options come before the files.

## Tests

```sh
go test ./...
```
