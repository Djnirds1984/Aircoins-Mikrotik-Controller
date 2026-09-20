# Development conventions

## What this project deliberately does not use

* **No RADIUS.** RouterOS authenticates hotspot users from its own local user
  database. The panel provisions those users and completes logins through the
  API. This is a decided architectural point, not an open question: it means the
  router keeps enforcing limits when the panel is offline.
* **No JavaScript build step.** The panel is server rendered Go templates plus
  one static `app.js`. The admin panel sets `script-src 'self'`, so inline event
  handlers are not allowed; use data attributes and `app.js`.
* **No CGO.** SQLite is accessed through `modernc.org/sqlite` so every target
  cross-compiles with `CGO_ENABLED=0`. Do not introduce `mattn/go-sqlite3`.

## RouterOS specifics worth remembering

* `/ip/hotspot/active/login` accepts exactly `ip`, `mac-address`, `user`,
  `password` and `domain`. Empty values must be omitted, because RouterOS treats
  an empty attribute as a syntax error.
* Per-user limits are `limit-uptime` and the `limit-bytes-*` trio. `rate-limit`,
  `shared-users` and `session-timeout` are **user profile** settings, so a panel
  "Plan" maps onto `/ip/hotspot/user/profile`.
* RouterOS 7 has no `comment` argument on `/ip/hotspot/user` or
  `/ip/hotspot/ip-binding`.
* `/ip/hotspot/host` exposes an `authorized` flag, which is the most reliable
  signal that a login actually took effect.
* The API cannot write files. Portal stubs go over FTP or SFTP into the router's
  flash and are selected with `html-directory-override`.
* A missing menu (`no such command`) is normal for some read commands on some
  devices: `/system/routerboard` and `/system/license` are absent on CHR and x86,
  and `/system/device-mode` is absent on RouterOS 6. Treat those as "not
  applicable" rather than as failures.

## Code layout rules

* `internal/domain` has no imports from other internal packages. It is the
  contract between the store, the RouterOS client and the HTTP layer.
* All SQL lives in `internal/store`. Handlers never touch `database/sql`.
* All RouterOS commands live in `internal/routeros`. Handlers never build API
  sentences.
* Every error surfaced to a user must be actionable. Probe checks carry a `Fix`
  string, and RouterOS traps are classified into sentinel errors
  (`ErrAuth`, `ErrPermission`, `ErrHostUnknown`, `ErrNoMenu`) rather than shown
  as raw text.
* Timestamps are stored in UTC as `YYYY-MM-DD HH:MM:SS` and parsed explicitly, so
  behaviour does not depend on driver defaults. Nullable columns are scanned into
  `sql.NullString`, not `string`.

## Testing

* `internal/routeros` tests use the external package `routeros_test`, because
  `faketos` imports `routeros` and an internal test package would create a cycle.
* Database tests use `newTestEnv` in `internal/store/store_test.go`, which opens
  a migrated SQLite database in a temporary directory.
* Anything that mutates a router must clean up even when it fails, and must be
  covered by a test asserting the cleanup happened.


