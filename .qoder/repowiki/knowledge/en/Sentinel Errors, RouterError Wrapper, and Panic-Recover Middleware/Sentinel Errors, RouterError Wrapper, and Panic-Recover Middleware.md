---
kind: error_handling
name: Sentinel Errors, RouterError Wrapper, and Panic-Recover Middleware
category: error_handling
scope:
    - '**'
source_files:
    - handlers/mikrotik.go
    - handlers/handlers.go
    - handlers/api.go
    - database/admin_users.go
    - database/coins.go
    - database/rates.go
    - database/routers.go
    - database/vouchers.go
    - database/portal_settings.go
---

## Approach

The codebase uses a layered Go error model:

1. **Package-level sentinel `error` values** (via `errors.New`) for domain conditions that callers should branch on.
2. **`fmt.Errorf` with `%w` wrapping** to attach per-call context while preserving the sentinel underneath so `errors.Is` still matches.
3. A single custom error type — `handlers.RouterError` — that wraps a sentinel plus endpoint/command metadata and implements both `Unwrap` and `Is` so it participates in `errors.Is`/`errors.As` chains.
4. An HTTP middleware (`handlers.Handler.recoverer`) that catches panics, logs them with stack traces via `log/slog`, and returns a 500 response instead of killing the process.

There is no third-party error library (no `pkg/errors`, `sentinel`, etc.); everything is built on the standard library `errors` and `fmt` packages.

## Key Files

- `handlers/mikrotik.go` — defines the RouterOS sentinel errors and the `RouterError` wrapper; also provides `routerErrorHint(err)` which maps each sentinel to an operator-friendly sentence used by the API/UI.
- `database/admin_users.go` — sentinel errors `ErrWeakPassword`, `ErrNoAdminUser`; demonstrates validation returning wrapped sentinels.
- `database/coins.go` — sentinels `ErrCoinPulseInvalid`, `ErrCoinPulseDuplicate`.
- `database/rates.go` — sentinels `ErrRateInvalid`, `ErrNoActiveRates`.
- `database/routers.go` — sentinel `ErrNotFound`.
- `database/vouchers.go` — sentinels `ErrVoucherNotRedeemable`, `ErrDuplicateVoucherCode`.
- `database/portal_settings.go` — sentinel `ErrPortalTheme`.
- `handlers/api.go` — JSON error envelope `apiError{Code, Message}` and helper `writeAPIError` used uniformly across every REST endpoint.
- `handlers/handlers.go` — `recoverer` middleware (panic → log + 500), `securityHeaders` (body size limits), `csrfGuard` (returns 403 on CSRF failure).
- `templates/error.html` — shared HTML error page rendered for non-API failures.

## Architecture & Conventions

### Sentinel errors
Each package exports package-level `var ErrXxx = errors.New("package: message")` constants. Callers match them with `errors.Is(err, database.ErrNotFound)`, never by string comparison. The messages are prefixed with the package name (e.g. `"database: ..."`, `"routeros: ..."`) so they are human-readable when logged.

### Wrapping
Validation and I/O functions return `fmt.Errorf("%w: <detail>", ErrXxx)` so the sentinel remains detectable while the returned value carries the offending value (e.g. `"database: invalid coin pulse: pulses must be between 0 and %d"`).

### `RouterError`
The `handlers.RouterError` struct holds `Endpoint`, `Command`, `Message`, `Sentinel`, and `cause`. Its `Error()` formats a human-readable string like `"message at /endpoint (command)"`. `Unwrap()` exposes the underlying transport error, and `Is()` short-circuits on the sentinel before delegating to `errors.Is(cause, target)`. This lets higher layers do `errors.Is(err, handlers.ErrRouterAuth)` regardless of whether the error came directly from the router or was wrapped by an HTTP handler.

### Error-to-HTTP mapping
- **REST API**: every non-2xx response goes through `h.writeAPIError(w, status, code, message)`, producing `{"code":"...","message":"..."}`. Domain sentinels are mapped to stable codes (e.g. `not_found`, `db_error`, `router_unreachable`, `unhealthy`).
- **HTML routes**: failures are turned into plain-text responses via `http.Error` or rendered through the shared `templates/error.html` template.
- **CSRF / body-size failures**: handled inline in middleware, returning 400/403 with fixed messages rather than propagating errors upward.

### Panic strategy
Panics are treated as unrecoverable programming errors. The only `recover()` in production code is `handlers/recoverer`, which:
1. Catches the panic.
2. Logs it with `h.log.Error("panic recovered", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))`.
3. Writes `http.Error(w, "internal server error", http.StatusInternalServerError)`.

A deliberate `panic(...)` exists in `database/admin_users.go::derivePassword` guarding against invalid PBKDF2 parameters (compile-time constants); its comment states this path is unreachable and panicking is intentional because it would break the security model.

### Database error wrapping
Database accessors call a `wrapDBError(op, err)` helper (defined in `database/database.go`) to prefix SQL errors with the operation name. Uniqueness violations are detected via `isUniqueViolation(err)` and re-wrapped into a user-facing message (see `admin_users.go` line 143). `sql.ErrNoRows` is converted to the package's `ErrNotFound` sentinel.

## Conventions & Constraints

- **Domain errors are exported as package-level sentinel variables**, not local `errors.New` calls inside functions. Every such sentinel lives at the top of its file alongside the types it belongs to.
- **Contextual details are added with `fmt.Errorf("%w: ...", sentinel)`, never by replacing the sentinel.** This preserves `errors.Is` compatibility.
- **RouterOS failures go through the `RouterError` wrapper** so the HTTP/API layer can distinguish auth, timeout, conflict, not-found, permission, and generic device errors without parsing device text.
- **The API surface always responds with the `apiError` JSON envelope** (`code` + `message`) for non-success status codes; there is no ad-hoc JSON error shape.
- **Panics are caught centrally** by the `recoverer` middleware; individual handlers do not recover. The only other `recover()` usage is in `handlers/icons_test.go`, a test helper.
- **Operator-facing messages come from `routerErrorHint`** for RouterOS errors, keeping user-visible text centralized rather than scattered across handlers.
- **Body-size DoS protection** is enforced in `securityHeaders` via `http.MaxBytesReader` (1 MiB default, larger for the portal background upload), returning a read error that the caller treats as a bad request.
- **CSRF enforcement** is done in the `csrfGuard` middleware; the coin-slot endpoint and `/portal/*` paths are explicitly exempted because they cannot participate in double-submit cookie flow.