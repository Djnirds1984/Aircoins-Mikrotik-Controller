---
kind: external_dependency
name: SQLite — embedded persistence
slug: sqlite-modernc
category: external_dependency
category_hints:
    - framework_behavior
scope:
    - '**'
source_files:
    - database/database.go
    - database/migrations.go
---

### SQLite (modernc.org/sqlite)
- All application state (routers, sessions, vouchers, rates, settings, stats) lives in a single SQLite file (`DB_PATH`, default `data/aircoins.db`).
- Built with `CGO_ENABLED=0` so there is no libsqlite3 dependency; the installer fetches Go and builds a pure-Go binary.
- Migrations are applied at startup through `database/migrations.go`.
- Tests use `:memory:` databases; production deployments back up `DB_PATH` together with `secret.key`.