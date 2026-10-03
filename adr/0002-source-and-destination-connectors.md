# ADR 0002: Source and destination connectors

- **Status:** Accepted
- **Date:** 2026-10-03
- **Supersedes:** answers open questions 1, 2 and 3 of [ADR 0001](0001-sync-mywhoosh-workouts-to-garmin.md)

## Context

ADR 0001 left open where workouts come from and how they reach Garmin Connect. We researched both sides in October 2026.

### Source: MyWhoosh

- MyWhoosh publishes no public API.
- Its apps use a private HTTP API that has been reverse engineered by community projects, most recently [marcelorodrigo/mywhoosh-to-garmin](https://github.com/marcelorodrigo/mywhoosh-to-garmin) (Python, active as of August 2026). The relevant calls are:
  - `POST https://services.mywhoosh.com/http-service/api/login` with email and password. Returns an access token, a refresh token and the rider's `WhooshId`.
  - `POST https://service14.mywhoosh.com/v2/rider/profile/activities` (Bearer token) with paging and `sortDate`. Returns the rider's activities, each with an `activityFileId`.
  - `POST https://service14.mywhoosh.com/v2/rider/profile/download-activity-file` (Bearer token) with `key` = `WhooshId` and `fileId` = `activityFileId`. Returns a short-lived pre-signed URL to the FIT file.
- MyWhoosh does not run on this Linux machine, so reading locally stored FIT files is not an option.
- Using Strava as the source was discarded: it adds a third account and OAuth app, and the Strava API does not return the original FIT file.

### Destination: Garmin Connect

- The official [Garmin Connect Developer Program](https://developer.garmin.com/gc-developer-program/activity-api/) is restricted to business partners and, according to the developer forums, onboarding has been paused during 2026. It is not available for a personal project.
- [garth](https://github.com/matin/garth), the library most unofficial clients relied on, is deprecated because Garmin changed its authentication flow.
- Maintained clients (for example [bpauli/gccli](https://github.com/bpauli/gccli), Go, MIT, active as of August 2026) now authenticate like this:
  1. The user signs in on `https://sso.garmin.com/sso/signin`, with the `service` parameter pointing to a local callback URL. Garmin redirects there with a single-use service ticket.
  2. The ticket is exchanged at `https://diauth.garmin.com/di-oauth2-service/oauth/token` for an OAuth2 access token and refresh token, using the Garmin Connect mobile app's public client id.
  3. The access token is refreshed with the same endpoint and `grant_type=refresh_token`.
- Activities are uploaded with `POST https://connectapi.garmin.com/upload-service/upload` (multipart field `file`, Bearer token). Garmin answers `409 Conflict` when the activity already exists.

## Decision

1. **Source connector:** a MyWhoosh client that uses the private API above. Credentials are read from the `MYWHOOSH_EMAIL` and `MYWHOOSH_PASSWORD` environment variables and are never written to disk.
2. **Destination connector:** a Garmin Connect client that:
   - signs in through the **browser** (`login` command). The user types credentials, and MFA if enabled, on Garmin's own page; the tool never sees the Garmin password. This also avoids the bot protection that blocks scripted logins.
   - stores the resulting tokens in a JSON file in the user's config directory, readable only by the user (`0600`).
   - refreshes the access token automatically, so `sync` can run unattended (for example from cron) until the refresh token expires.
   - uploads FIT files to `upload-service` and treats `409 Conflict` as "already uploaded".
3. **Duplicate detection:** a local state file records the MyWhoosh activity ids that were uploaded. Garmin's `409` response is a second line of defence.
4. **No third-party Go modules.** Everything uses the standard library, so the tool is a single static binary with no supply-chain surface. We write our own code and do not copy code from the projects above, whose licences differ (GPL-3.0 and MIT).
5. **Ports and adapters:** the sync logic depends on small `Source` and `Destination` interfaces, so connectors can be swapped (for example Strava as a source) without touching the sync logic.

## Consequences

- Both connectors depend on **unofficial, undocumented APIs**. Either side can break without notice when MyWhoosh or Garmin change their services. Errors must be explicit, and endpoints and client ids must be easy to update in one place.
- The Garmin client id of the mobile app may be rotated by Garmin. It is a constant in the Garmin connector.
- Garmin login needs a browser on the machine that runs `login`. Unattended runs only need the stored tokens.
- Files uploaded from MyWhoosh keep their original device information, so Garmin may not compute some metrics (training effect, training status). Rewriting the FIT device fields is possible but out of scope for this ADR.
- Using private APIs may go against the terms of service of MyWhoosh or Garmin. This is a personal tool, used only with the owner's own accounts and at a low request rate.
