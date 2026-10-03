# ADR 0001: Automatically sync MyWhoosh workouts to Garmin

- **Status:** Proposed
- **Date:** 2026-10-03
- **Project:** mywhoosh2garmin

## Context

Indoor cycling workouts are done with the MyWhoosh app. When a session ends, MyWhoosh automatically uploads it to Strava, but it has no integration with Garmin Connect.

As a result, MyWhoosh sessions do not appear in the Garmin account. This leaves the activity history and the training load calculated by Garmin incomplete.

## Problem

Some workouts sync to Strava but not to Garmin. Uploading them to Garmin Connect by hand, one by one, is repetitive and easy to forget.

## Goal

When a new workout session is recorded in MyWhoosh, it is uploaded automatically to the Garmin Connect account, without duplicating activities that were already uploaded.

## Decision

The first deliverable will be a **command-line tool written in Go**. It will:

1. Fetch new sessions from MyWhoosh (directly or via Strava, depending on what is feasible; see open questions).
2. Work out which ones are not yet in Garmin Connect.
3. Upload the missing ones to Garmin Connect, preferably as FIT files.
4. Remember which sessions have already been synced, so running the tool repeatedly never creates duplicates.

We start with a command-line tool because it is the simplest and fastest thing to validate. Later we can decide whether to run it on a schedule or add a web interface.

## Scope

**In scope:** a Go binary that is run manually (or from cron / a task scheduler) and syncs new MyWhoosh sessions to Garmin.

**Out of scope, for now:** web interface, running as a service, support for other training platforms, syncing in the opposite direction (Garmin to MyWhoosh).

## Options to investigate

Feasibility depends on how the data can be reached and how it can be uploaded. This must be verified before implementing:

- **Data source:**
  - A. Read sessions from MyWhoosh (exported or locally stored FIT files, or its service, if accessible).
  - B. Read activities from Strava via its API, since MyWhoosh already uploads them there.
- **Destination (Garmin Connect):**
  - A. Official Garmin developer API, if personal-use access can be obtained.
  - B. FIT file upload through the Garmin Connect web flow, authenticating with the account (unofficial and subject to change).
  - C. Assisted manual import as a minimum viable fallback if the above are not viable.

## Consequences

- **Pros:** a single binary with no runtime dependencies, easy to distribute and schedule.
- **Cons:** if the Garmin upload relies on an unofficial flow, it may break when Garmin changes its service, and authentication and credential storage must be handled carefully.
- We need a way to persist the sync state (for example, a local file with activity identifiers).

## Open questions

1. Where do sessions come from: MyWhoosh directly or Strava?
2. Which upload method to Garmin is viable for a personal account?
3. How are credentials and tokens stored securely?
4. How often should the sync run?
5. How are duplicates detected (start time, duration, identifier)?

## Next steps

1. Investigate the feasibility of the source and the destination (questions 1 and 2).
2. Create the Go project skeleton (`go mod init`, command structure).
3. Implement session reading and duplicate detection first; then the Garmin upload.
