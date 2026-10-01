---
kind: logging_system
name: Structured Logging via Go's log/slog with Text Handler
category: logging_system
scope:
    - '**'
source_files:
    - main.go
    - handlers/handlers.go
    - database/database.go
---

## System Overview

The Aircoins MikroTik Controller uses Go's standard library `log/slog` (Go 1.21+) as its sole logging framework. There is no third-party logger, no custom logger abstraction, and no log-level configuration flag — the application creates one `*slog.Logger` instance in `main.go` and injects it into both the `database` and `handlers` packages.

## Initialization and Sink

In `main.go`, the logger is created at startup:

```go
logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
```

It writes plain text to `os.Stdout`. No JSON handler, no file sink, no rotation, no structured attributes are configured at the root level. The second argument (`nil`) means default options are used; there is no `Level` filter set, so all levels (`Debug`, `Info`, `Warn`, `Error`) pass through by default.

The logger is then attached to both subsystems:

- `cfg.DB.Logger = logger` — passed into `database.Config`
- `cfg.Handler.Logger = logger` — passed into `handlers.Config`

## Logger Propagation

Both `database.Config` and `handlers.Config` accept an optional `Logger *slog.Logger`. Their `withDefaults()` methods replace a nil logger with `slog.New(slog.DiscardHandler)` so that tests or callers that omit the logger get silent behavior rather than panics:

- `handlers/handlers.go:112-114`: `if c.Logger == nil { c.Logger = slog.New(slog.DiscardHandler) }`
- `database/database.go:56-58`: same pattern for database config

Each package stores the logger on its own struct (`handlers.Handler.log`, `database.DB.log`) and calls it directly — there is no global logger variable.

## Log Levels Used

The codebase uses four slog levels consistently:

| Level | Usage |
|-------|-------|
| `Info` | Startup/shutdown events, successful admin sign-in/sign-out, HTTP access logs, migration application, panel account creation |
| `Warn` | Admin login throttling, failed login attempts, CSRF token rejection, missing router references, generated initial password |
| `Error` | All error paths (DB operations, API failures, template rendering, session revocation, JSON write errors) |
| `Debug` | Traffic interface resolution, portal parameter forwarding |

There is no `Fatal` usage — fatal exits go through `os.Exit(1)` from `main` after printing to `os.Stderr` (e.g., `runPasswd`, `mustGeneratePassword`).

## Structured Fields Convention

Every log call uses key/value pairs via slog's variadic args. Common field names across the codebase include:

- `error` / `err` — wrapped error values
- `remote` — client IP (from `clientIP(r)`)
- `path` — request URL path
- `method` — HTTP method
- `status` — HTTP response status
- `bytes` — response body size
- `duration_ms` — request duration in milliseconds
- `id` — router/voucher/session identifiers
- `account` / `username` — operator account name
- `version` — binary version string
- `addr` — server listen address
- `stack` — panic stack trace

Example patterns from `handlers/api.go`:
```go
h.log.Error("api routers list", "error", err)
h.log.Error("api voucher create", "error", err)
```

From `handlers/handlers.go` access log:
```go
h.log.Info("http",
    "method", r.Method,
    "path", r.URL.Path,
    "status", rec.status,
    "bytes", rec.bytes,
    "remote", clientIP(r),
    "duration_ms", time.Since(start).Milliseconds(),
)
```

## Request Logging Middleware

`handlers/logRequests` wraps every request and emits a single structured access log line containing method, path, status, bytes, remote IP, and duration. It uses a small `statusRecorder` wrapper around `http.ResponseWriter` to capture the written status code.

## Panic Recovery

The outermost middleware `recoverer` catches panics, logs them with `h.log.Error("panic recovered", ...)` including `path`, `panic` value, and full `debug.Stack()`, then returns a 500 Internal Server Error without crashing the process.

## Database Logging

Database operations log via `db.log` (the injected logger). Examples:
- `database/migrations.go:321`: `db.log.Info("applied database migration", "version", m.version, "name", m.name)`
- `database/routers.go:412`: `db.log.Error("cannot encrypt router password", "error", err)`

## Notable Conventions

1. **No log-level CLI flag** — the application does not expose a way to change verbosity at runtime or via environment variables.
2. **Discard handler fallback** — any package accepting a logger defaults to `slog.DiscardHandler` when none is provided, making logging opt-in per subsystem.
3. **No sensitive data in logs** — passwords are deliberately base32-encoded before being logged (see `randomPassword` comment: "keeps it free of characters that are easy to misread when copied off a log"), and the initial password is only emitted once during bootstrap.
4. **All logs go to stdout** — no file output, no syslog integration, no log rotation is built in.
5. **Context-free logger** — the logger is a struct field, not derived from `context.Context`; there is no request-scoped logger with per-request correlation IDs.