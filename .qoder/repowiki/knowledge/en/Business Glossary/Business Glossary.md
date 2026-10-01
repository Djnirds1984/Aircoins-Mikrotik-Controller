---
kind: business_term
name: Business Glossary
category: business_term
scope:
    - '**'
---

### captive portal
- Definition：The public-facing landing page served at `/` (and `/portal/login`, `/portal/status`) that every MikroTik hotspot client sees before authentication; distinct from the operator panel at `/admin/`.
- Aliases：portal

### operator panel
- Definition：The authenticated admin UI at `/admin/` providing dashboard, routers, network, vouchers, sessions, tools, and settings views. Requires username/password sign-in; first-boot password is generated if `ADMIN_PASSWORD` is unset.
- Aliases：panel、dashboard

### voucher
- Definition：A prepaid access token generated in batches (quantity, prefix, groups, profile, time/data/device limits, price, max uses, optional immediate provisioning). Provisioned on the router and tracked in the local ledger so a dropped API connection does not burn a key.
- Aliases：vouchers

### coin slot
- Definition：Optional piso Wi-Fi hardware accessory (NodeMCU/ESP8266 or ESP32 attached to a multi-coin acceptor) that reports coin pulses to the controller, converting them into hotspot access time for the customer.
- Aliases：piso Wi-Fi coin slot、coin acceptor

### hotspot login page
- Definition：The MikroTik-generated form whose action is pointed at the controller's portal URL, forwarding `mac`, `ip`, `username`, `link-login`, `link-login-only`, `link-orig`, `server-name`, `error` so the controller can resolve the correct router and complete the redirect.
- Aliases：login form

### connection method
- Definition：Per-router selection between four modes: `REST over HTTPS (www-ssl)`, `REST over HTTP (www)`, `API (8728)`, `API-SSL (8729)`, or `Auto` (tries REST first, then binary API, remembering whichever answered).
- Aliases：router connection method

### web port for REST
- Definition：Configuration field on the router entry specifying the MikroTik `www`/`www-ssl` port used by the REST API; left blank for HTTPS (443), must be explicitly set for HTTP (e.g. 80). Distinct from the controller's own listen port.
- Aliases：REST web port

### portal tag
- Definition：A label on a registered router used to match incoming hotspot requests whose `server-name` resolves to that router; also supports a default portal fallback when no `server-name` is present.
- Aliases：portal tag

### master key
- Definition：The AES-256-GCM encryption key loaded from `SECRET_KEY_PATH` (or overridden by `AIRCOINS_SECRET_KEY`) used to encrypt router passwords at rest; without it a stolen database cannot decrypt stored credentials.
- Aliases：secret key、master key file

### session
- Definition：An operator-panel login represented by a 32-byte random token cookie (`HttpOnly`, `SameSite=Lax`) whose SHA-256 hash is stored in the database; logout deletes the row and changing the password invalidates all sessions.
- Aliases：panel session
