# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Single-package Go CLI (`package main`, flat layout) that copies Google Contacts from a source Google account to a destination account via the People API without creating duplicates, optionally deleting copied contacts from the source. README.md documents user-facing flags and behavior; keep it in sync when changing flags or dedupe rules. All code, docs and user-facing output are in English.

## Commands

```sh
go build -o synccontacts .
go test ./...
go test -run TestName ./...      # single test
./synccontacts -dry-run          # safe simulation
./synccontacts -check [-v] [backup_source_*.json]   # compare backups against destination
```

Running for real needs `credentials.json` (OAuth desktop client) and interactive browser login; tokens are stored in `os.UserConfigDir()/synccontacts/`.

## Architecture

- `main.go` — flags (`options`) and `run()`, the orchestration: authenticate both accounts, refuse if same account, list both sides, build the destination index, classify each source contact, confirm, batch-create, then optional delete from source.
- `auth.go` — OAuth loopback login per role (source/destination), token persistence, email lookup. Source uses read-only scope unless `-delete-source` is set (which forces re-login with write scope). Expired tokens trigger re-login.
- `dedupe.go` — `index` of destination contacts keyed by email (case-insensitive), phone (last 8 digits), accent/case-folded name, and `clientData` source-id. `match()` returns a `matchKind`; name-only matches are weaker than email/phone matches.
- `contacts.go` — People API helpers: listing, `copyPerson` (strips ids/metadata, stores source id in `clientData` so reruns are idempotent), batch create/delete (with `withRetry`), `writeBackup`, and `groupMapper` for creating/mapping labels (contact groups) between accounts.
- `check.go` — `-check` mode: loads `backup_source_*.json` files, classifies each against the destination (copied / email-phone exists / name only / missing) and reports data lost via `lostData`.

## Safety invariants (preserve when editing)

- Source account is read-only unless `-delete-source`.
- Contacts matched only by name are never deleted from source; `-delete-duplicates` is required to delete email/phone-matched ones.
- If creation fails partway, nothing is deleted. Deletion needs its own confirmation (unless `-yes`) and writes `backup_source_<timestamp>.json` first.
- `credentials.json`, tokens, `backup_*.json` (personal data) and the built binary are gitignored; never commit them.
