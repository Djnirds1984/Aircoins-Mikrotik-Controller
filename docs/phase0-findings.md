# Phase 0 findings â€” RouterOS API validation

This document is the acceptance record for Phase 0. It is filled in as the
`aircoins-probe` command is run against real hardware, and each entry states what
was assumed, what was tested, and what changed as a result.

## How to run the harness

```
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass secret
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass secret -allow-write -json
```

`-allow-write` is required only for the write-permission check. Without it the
probe never mutates router configuration.

Simulated scenarios, for reproducing a finding without hardware:

```
go run ./cmd/aircoins-probe -demo=ok
go run ./cmd/aircoins-probe -demo=http-chap
go run ./cmd/aircoins-probe -demo=read-only
go run ./cmd/aircoins-probe -demo=no-hotspot
go run ./cmd/aircoins-probe -demo=device-mode
go run ./cmd/aircoins-probe -demo=small-flash
go run ./cmd/aircoins-probe -demo=clock-skew
```

## S1 â€” API connection and device facts

**Assumption:** RouterOS 7 exposes identity, resource, routerboard, clock, NTP and
license over the API, and `api` on 8728 / `api-ssl` on 8729 are the service names
to check.

**Result:** confirmed against the simulated device; awaiting a real device.

* `/system/routerboard/print` does not exist on CHR and x86, so `ReadBoard`
  tolerates the missing menu and reports `Present: false` rather than failing.
* `/system/license/print` is CHR and x86 only, handled the same way.
* `/system/device-mode/print` does not exist on RouterOS 6, so a missing menu is
  reported as "unrestricted" rather than as a failure.
* Free flash can be reported as a byte count or a suffixed string depending on
  version and board, so `ParseSize` accepts both.

## S2 â€” Panel driven login

**Assumption:** `/ip/hotspot/active/login` accepts exactly `ip`, `mac-address`,
`user`, `password` and `domain`, and succeeds when the client already has a
hotspot host entry.

**Result:** confirmed by the CLI reference; awaiting a real device.

* The documented failure mode is the `!trap` `unknown host IP 0.0.0.0`, which is
  what the device reports when the client is not in the host table. The panel
  maps that to `routeros.ErrHostUnknown` so the portal can say something useful.
* An empty attribute value is a syntax error on the wire, so `LoginRequest.Args`
  omits empty fields entirely. `TestLoginRequestArgsIsWellFormed` locks the wire
  format down.

## S3 â€” login-by must include http-pap

**Assumption:** a panel login is PAP, so `http-chap` alone rejects it.

**Result:** confirmed by the CLI reference; awaiting a real device.

* `SupportsPAPLogin` checks the `login-by` list, and the probe turns a missing
  `http-pap` into a FAIL with the exact command to fix it.
* Phase 3 must set `login-by=http-pap,cookie,trial` during provisioning while
  preserving any other methods the operator already relies on.

## S4 to S6 â€” Limits, enforcement and the counter reset trap

**Assumption:** per-user limits are `limit-uptime`, `limit-bytes-in`,
`limit-bytes-out` and `limit-bytes-total`; rate limits and device limits live on
the user *profile*, not on the user.

**Result:** confirmed by the CLI reference; awaiting a real device.

**Open question for a real device:** whether deleting and re-creating a hotspot
user resets its accumulated `uptime` and byte counters. If it does, a user could
reset their own quota by forcing a re-provision. The mitigations already in the
schema are that the panel stores authoritative `used_uptime_seconds` and byte
counts per voucher, and that on re-creation it writes
`limit = plan_limit - already_used`.

This must be verified against a real device before Phase 4 is signed off,
because it determines whether re-creation is safe at all or whether the panel
must keep the router-side user alive until the voucher is exhausted.

## S7 â€” Walled garden to an external portal

**Assumption:** a pre-authentication client can reach the panel only if the panel
appears in both the HTTP walled garden (Host header match, plain HTTP) and the IP
walled garden (address match, for anything encrypted).

**Result:** confirmed by the CLI reference; awaiting a real device.

* `action=allow` for the HTTP table, `action=accept` for the IP table. The two
  helpers enforce this instead of relying on the caller.
* DNS to the gateway must also be allowed pre-auth if the portal is addressed by
  a host name, which is why an IP based portal URL is the default.

## S8 â€” Portal template variables

**Assumption:** the login stub uses `$(mac)`, `$(ip)`, `$(link-orig-esc)`,
`$(error)` and `$(server-name)`.

**Result:** not run. The exact spellings must be confirmed by dumping the
reference templates from a real device:

```
/ip hotspot reset-html
```

then reading the generated files over FTP and comparing variable names. This is a
Phase 3 prerequisite, because a wrong variable name fails silently in the browser.

## S9 â€” Pushing the login stub

**Assumption:** the API cannot write files, so the stub is uploaded over FTP or
SFTP into `flash/hotspot-aircoins/` and selected with
`html-directory-override`.

**Result:** confirmed by the CLI reference; awaiting a real device.

`/ip hotspot reset-html` is the documented rollback, and the panel stores a
`stub_hash` so drift can be detected.

## S10 â€” Browser behaviour after an API login

**Assumption:** after `/ip/hotspot/active/login` the client is authorised by IP
and MAC in the hotspot tables, so redirecting the browser to `$(link-orig)` works
even though no hotspot cookie was set by the router's own login page.

**Result:** not run. This needs a real device plus an iPhone, an Android device
and a Windows machine, because the OS captive portal sheet must dismiss itself.

**Contingency if the assumption is wrong:** hand the client a one-time signed URL
that posts to the router's own `/login` endpoint from its own browser, then
observe the result over the API.

## S11 â€” Usage polling

**Assumption:** `/ip/hotspot/active/print` and `/ip/hotspot/user/print` carry
`uptime`, `bytes-in` and `bytes-out`, and the active table updates continuously
while the user table accumulates across sessions.

**Result:** confirmed by the CLI reference; awaiting a real device.

The Phase 6 poller should use `.proplist` plus a query filter so a busy router is
not dumped on every tick.

## S12 â€” Operational guards

**Assumption:** hotspot can be blocked by `device-mode`, the hotspot service ports
64872 to 64875 must not be filtered, and session timers depend on the device
clock.

**Result:** confirmed by the CLI reference; awaiting a real device.

The probe covers the first and third. The firewall rule is a documentation item
for the provisioning wizard.

## Summary

| Item | Status | Blocks |
|---|---|---|
| S1 device facts | confirmed (simulated) | nothing |
| S2 API login | confirmed (reference) | Phase 5 |
| S3 http-pap | confirmed (reference) | Phase 3 |
| S4 to S6 limits and counter reset | open | Phase 4 |
| S7 walled garden | confirmed (reference) | Phase 5 |
| S8 template variables | not run | Phase 3 |
| S9 stub upload | confirmed (reference) | Phase 3 |
| S10 post-login browser behaviour | not run | Phase 5 |
| S11 usage polling | confirmed (reference) | Phase 6 |
| S12 operational guards | confirmed (reference) | Phase 3 |

