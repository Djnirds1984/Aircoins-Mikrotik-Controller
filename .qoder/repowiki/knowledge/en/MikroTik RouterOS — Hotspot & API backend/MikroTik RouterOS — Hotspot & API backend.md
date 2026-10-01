---
kind: external_dependency
name: MikroTik RouterOS — Hotspot & API backend
slug: mikrotik-routeros
category: external_dependency
category_hints:
    - vendor_identity
    - client_constraint
scope:
    - '**'
source_files:
    - handlers/mikrotik_rest.go
    - handlers/mikrotik_ros.go
    - handlers/mikrotik_hotspot.go
---

### MikroTik RouterOS
- The controller manages one or more MikroTik routers as hotspots: active-session listing, disconnect, IP-binding block/unblock, and voucher provisioning.
- RouterOS credentials (user/password) are stored AES-256-GCM encrypted at rest behind a master key in `SECRET_KEY_PATH`; without it a stolen `.db` is useless.
- The hotspot login page must POST to the portal preserving `mac`, `ip`, `link-login`, `link-login-only`, `link-orig`, `server-name`, `error` so the controller can resolve the correct router and forward the redirect.
- Verify exact RouterOS resource names and REST endpoints against the official RouterOS docs; the code uses both the binary and REST client libraries.