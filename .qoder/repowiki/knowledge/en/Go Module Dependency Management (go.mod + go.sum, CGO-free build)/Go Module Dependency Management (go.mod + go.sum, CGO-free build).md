---
kind: dependency_management
name: Go Module Dependency Management (go.mod + go.sum, CGO-free build)
category: dependency_management
scope:
    - '**'
source_files:
    - go.mod
    - go.sum
    - install.sh
---

## Approach

The repository uses the standard Go module system (`go.mod` / `go.sum`) for declaring and pinning third-party dependencies. There is no vendoring directory, no private Go proxy configuration, and no custom dependency-update tooling — dependency resolution is delegated to `go mod` against the public Go module proxy.

## Key Files

- `go.mod` — declares the module path `github.com/djnirds1984/aircoins-mikrotik-controller`, pins the Go toolchain version to `1.26.5`, and lists two direct dependencies: `github.com/go-routeros/routeros/v3 v3.0.1` (RouterOS API client) and `modernc.org/sqlite v1.59.0` (pure-Go SQLite). Indirect deps are transitively pulled in by these two packages.
- `go.sum` — cryptographic checksums for every resolved module and transitive dependency; used by `go mod verify` and the Go toolchain to ensure reproducible builds.
- `install.sh` — the production install script invokes the build with `CGO_ENABLED=0 GOFLAGS="-p=$BUILD_JOBS"`, forcing a fully static, CGO-free binary that can link the pure-Go `modernc.org/sqlite` implementation without any system C libraries.

## Architecture and Conventions

- **Minimal dependency surface**: The project intentionally keeps its direct dependency list to two packages. All other functionality (HTTP server, HTML templating, crypto, UUID generation, human-readable formatting) comes from the Go standard library or indirect dependencies of the two direct ones.
- **Versioned import paths**: The RouterOS client uses the `/v3` major-version suffix (`github.com/go-routeros/routeros/v3`), following Go's semantic import versioning convention so that breaking changes do not silently upgrade the consumer.
- **Pure-Go SQLite**: `modernc.org/sqlite` is chosen specifically because it is implemented entirely in Go and has no CGO requirements, enabling the `CGO_ENABLED=0` build flag in `install.sh`. This avoids bundling a C compiler or libc into the release artifact.
- **No vendor directory**: Dependencies are fetched on-demand from the Go module proxy; there is no `vendor/` tree committed to the repo.
- **No private registry / GOPRIVATE**: No `GOPRIVATE`, `GONOSUMCHECK`, `GONOSUMDB`, or `GOFLAGS` entries appear in `go.mod`; all modules are expected to be publicly available on `proxy.golang.org`.

## Constraints and Rules

- **Build must be CGO-free**: `install.sh` line 317 sets `CGO_ENABLED=0` at build time, which enforces that only pure-Go dependencies (or those with CGO disabled) may be linked. This is an enforced constraint of the release build, not merely a convention.
- **Go toolchain pinned**: `go.mod` specifies `go 1.26.5`, so the same toolchain is required to interpret the module graph and resolve versions consistently across environments.
- **Checksums locked**: `go.sum` is committed, so CI or downstream consumers get deterministic dependency resolution via the Go module checksum database rather than trusting unverified network responses.
- **No vendoring policy documented**: The repository does not contain a `vendor/` directory nor any documentation prescribing vendoring; the observed behavior is to rely on `go mod` fetching from the public proxy.