# Security Hardening and Runnable Test Baseline — Design

Date: 2026-10-16
Status: approved by owner 2026-10-16

## Intent

ThingsToDo is becoming AI-first: an AI agent (Hermes Agent, over API/MCP) will create and manage tasks, and separate widget apps will read slices of data over the API. The built-in web UI becomes secondary. The owner runs the app in Docker on a VPS reachable from the internet.

An audit on 2026-10-16 found exploitable holes and a test suite that does not run from a clean clone. This project closes the holes and makes both test suites runnable locally and in CI. It is project 1 of 4, and the next three build on it:

1. **Security hardening and runnable tests (this document).**
2. The server becomes the single source of business rules: views, dates, filters, timezone.
3. An AI and widget API: scoped tokens, CORS, input validation, filter-only queries, OpenAPI, a new MCP.
4. Built-in UI bugs and frontend test coverage.

## Goals

- Close every vulnerability listed below. Each fix starts with a failing test that reproduces the hole (RED), then the fix (GREEN).
- `go test ./...` and `go vet ./...` pass from a clean clone with no manual steps.
- `cd frontend && npm ci && npm test` passes, and CI runs it.
- Auth middleware has tests for all four modes (`builtin`, `oidc`, `proxy`, `none`) and the API-key path.

## Non-goals (deferred to later projects)

- Scoped, read-only or per-client tokens; hiding `ntfy_access_token` from API-key holders. Deferred to project 3.
- CORS. Deferred to project 3.
- Date, time and enum validation, atomic task create, FTS escaping, and structured validation errors. Deferred to project 3.
- Change-feed fixes, recurrence `after_completion`, reminder timezone parsing, and UTC/local skew. Deferred to project 2.
- Broad coverage of the scheduler, push, views, schedules, saved filters and frontend local queries. Each gets tested in the project that changes it: 2 for server rules, 4 for frontend.
- Multi-user support. The app stays single-user.

## Anti-goals

- Breaking the existing web UI, the CLI (`ttd`) or the MCP server (`mcp/`) for legitimate use.
- Adding new dependencies. Everything here uses the stdlib, Go 1.24, or packages already in `go.mod`.
- Disabling checks or tests to get green.

## Constraints

- Go 1.24 (`go.mod`), `github.com/golang-jwt/jwt/v5` v5.3.1, chi v5.2.5, React 19.2.
- Single-user, single SQLite DB in `/data`. Attachments live in `/data/attachments` (`config.go:46`), and the DB lives next to them in `/data`.
- Deployment: Docker on an internet-facing VPS, presumably behind a TLS reverse proxy (assumed, not stated by the owner).
- `docs/api.md` must be updated for any behavior change (project rule in `CLAUDE.md`).

## Fixes

Each item: the hole, the evidence, and the required behavior after the fix.

### S1. Attachment path traversal (critical, reproduced)

- **Hole.** A JSON `POST /api/tasks/{id}/attachments` request, or a `sync/push` attachment `create`, accepts `{"type":"file","url":"../thingstodo.db"}`. Afterwards, `GET /api/attachments/{id}/file` streams the DB, and `DELETE` runs `os.Remove` on that path. See `attachments.go:52-64,163,190` and `sync.go:1024-1053`.
- **Required behavior:**
  - JSON create and sync create accept only links. An absent or empty `type` still defaults to `link`, as today; `mcp/src/tools/attachments.ts` relies on this. Any other explicit type gets 400 `VALIDATION` from the REST path and an `error` result from sync.
  - File attachments are created only through the multipart upload, which already generates the stored name server-side (`attachments.go:87-90`).
  - As defense in depth, `Download` and `Delete` open and remove files through `os.OpenRoot(attachmentsPath)` / `os.Root` (Go 1.24). Any name that escapes the root fails: 404 on download; on delete, the row is removed and the file is left alone.
- **Verified:** the web UI only ever creates `link` attachments through the local mutation and sync path (`hooks/queries.ts:761`), so restricting JSON and sync to links breaks nothing.

### S2. JWT authentication bypass (critical, reproduced)

- **Hole.** The cookie path in `middleware/auth.go:75-91` accepts any HS256-signed JWT. It does not restrict `alg`, does not require `exp`, and does not require `sub`. `GET /api/auth/oidc/login` hands an anonymous caller exactly such a token: the `oidc_state` cookie, signed with `JWT_SECRET` and carrying a `state` claim. Replaying that cookie as `token=` opens the API.
- **Required behavior.** Parse with:

  ```go
  jwt.WithValidMethods([]string{"HS256"})
  jwt.WithExpirationRequired()
  ```

  - Require a non-empty `sub` that resolves to an existing user, otherwise 401.
  - The OIDC state token must not be usable as a session token. Requiring `sub` already closes this, because the state token has no `sub`. Also add a distinguishing claim, e.g. `"typ":"oidc_state"`, that `verifyState` checks and the middleware rejects.
- **Source:** golang-jwt v5 does not restrict alg or require exp by default; `WithValidMethods` and `WithExpirationRequired` (added in v5.1.0) do. pkg.go.dev/github.com/golang-jwt/jwt/v5, checked 2026-10-16.

### S3. OIDC account takeover (high, found in code)

- **Hole.** `oidc.go:149-172`: when the IdP returns an email that is not the current username, the single existing user is renamed to that email. This happens on every login with a new email, not just the first, so any account at the IdP takes over the app. `email_verified` is not checked. The README promises that other accounts are rejected, but that rejection does not exist.
- **Required behavior:**
  - Add a new required env var `OIDC_ALLOWED_EMAIL`. Startup fails when `AUTH_MODE=oidc` and it is empty.
  - Read `email` and `email_verified` from the verified ID token. If the ID token has no `email`, read both from the userinfo endpoint, as `oidc.go:122-133` does today. Keep that fallback.
  - Accept the userinfo response only when its `sub` equals the ID token's `sub`, per OIDC Core §5.3.4. Take `email` and `email_verified` from the same source; never mix the two.
  - Login proceeds only when the email equals `OIDC_ALLOWED_EMAIL`, case-insensitive, and `email_verified` is `true`. A missing or false `email_verified` gets 403.
  - Why keep the fallback: since 4.39, Authelia (named in the README) puts `email` and `email_verified` only in userinfo by default and recommends that clients read them there. Source: authelia.com/integration/openid-connect/openid-connect-1.0-claims/, checked 2026-10-16. Userinfo is fetched server-to-server with the access token from the code exchange, so it is as trustworthy as the ID token.
  - On first login the single user is linked to that email, as today. Any other email gets 403.
  - Document the variable in README and `.env.example`.
- **Note:** this is a breaking config change for anyone already running `AUTH_MODE=oidc`. That is the designer's call (see "Calls made").

### S4. No panic recovery; sync push panics on wrong JSON types (high, reproduced)

- **Hole.** `sync.go` uses unchecked `v.(string)` assertions on `change.Data`, at lines 350-371, 442-466, 601-609, 652-667 and similar. A wrong type panics the handler. There is no recover middleware (`router.go:30`), so the connection drops mid-batch and the batch is partially applied.
- **Required behavior:**
  - Replace unchecked assertions in `sync.go` with checked ones. A wrong type yields an `error` result for that change and processing continues.
  - Add a small custom recover middleware as a backstop, as the first middleware in `router.go`. It logs the panic with its stack and returns 500 `{"error":"internal error","code":"INTERNAL"}`. Do not use chi's `Recoverer`: it does not write this JSON body.

### S5. Internal error text leaks to clients (medium, found in code)

- **Hole.** Handlers pass `err.Error()` into `writeError` for 500s, which exposes SQLite messages and schema details.
- **Required behavior:**
  - In `writeError` (`helpers.go`), for status ≥ 500, log the original message server-side and send `{"error":"internal error","code":"INTERNAL"}`. Fix it once, in the shared helper, not per call site.
  - `sync/push` returns 200 with per-change `error` strings, and today these carry `err.Error()` from repositories. Replace repository and DB errors with `internal error`, and log the original.
  - Keep validation messages as they are, including `unsupported entity type`: `frontend/src/sync/push.ts:108-121` matches that text.

### S6. No request body size limit (medium, found in code)

- **Hole.** JSON bodies are unbounded (`helpers.go:18-21`). Only multipart upload is capped.
- **Required behavior:**
  - A router-level middleware wraps every request body in `http.MaxBytesReader`, limit 10 MiB, regardless of `Content-Type`. The only exempt route is the file upload route, `POST /api/tasks/{id}/attachments`. It keeps its existing `MAX_UPLOAD_SIZE` cap, applied in the handler. That route also serves JSON link creates, so the handler's JSON branch applies the same 10 MiB limit itself.
  - When a handler's read hits the limit, the response is 413 with the JSON error body. `decodeJSON` and `decodeJSONWithRaw` map `*http.MaxBytesError` to 413.
  - Bytes after the first JSON value are not read. That is acceptable, because unread bytes cost no server memory.

### S7. Session cookies lack `Secure` (medium, found in code)

- **Hole.** `token` and `oidc_state` cookies are set without `Secure` (`auth.go:59-66`, `oidc.go`). Behind a TLS proxy on the internet they should be Secure.
- **Required behavior:** set `Secure: true` when `r.TLS != nil` or `X-Forwarded-Proto` is `https`. Plain-HTTP local dev keeps working.

### S8. No brute-force protection on password login (medium, found in code)

- **Hole.** `POST /api/auth/login` (`auth.go:23-58`) has no attempt limit, and the server is internet-facing.
- **Required behavior:**
  - Use an in-process limiter on failed logins, keyed by client IP: after 10 failures from one IP within 15 minutes, `POST /api/auth/login` from that IP returns 429 `RATE_LIMITED` until the window expires.
  - Client IP is `RemoteAddr`. When `RemoteAddr` is loopback or private (RFC 1918, RFC 4193), meaning a local reverse proxy forwarded the request, use the rightmost `X-Forwarded-For` entry instead.
  - A global counter was rejected: an attacker could send 10 bad passwords every 15 minutes and lock the owner out indefinitely.
  - It applies only to password login. The API key is a long random secret compared in constant time.
  - Prune expired entries so the map cannot grow without bound.
  - Mark it `ponytail:` with its ceiling: in-memory, reset on restart, and IP rotation evades it.

### S9. `javascript:` and other script URLs in link attachments (low, found in code)

- **Hole.** Link URLs are stored as given (`attachments.go:60-63`). The web UI renders them as `href` (`TaskDetail.tsx:754`). React 19 blocks `javascript:` URLs in `href` at runtime, so the web UI is not exploitable. Widgets and other API clients may still render them raw.
- **Required behavior:**
  - Trim the link URL and parse it with `net/url.Parse`. A parse error (it rejects ASCII control characters such as an embedded tab or newline) gets 400 `VALIDATION` from REST and an `error` result from sync.
  - The same response applies when the parsed scheme, case-folded, is `javascript`, `data`, `vbscript` or `file`.
  - Tests cover `java\tscript:` and an embedded newline.
  - This is a denylist, not an allowlist, because app links such as `obsidian://` and `siyuan://` are legitimate (the app has reserved tags for both).
- **Source:** React 19 Upgrade Guide, "Error for javascript URLs in src and href" (react.dev/blog/2024/04/25/react-19-upgrade-guide), checked 2026-10-16.

### S10. Deployment guidance for header-trust modes (low, docs only)

`AUTH_MODE=proxy` trusts the `Remote-User` header, and `AUTH_MODE=none` has no auth. If the container port is reachable directly from the internet, either mode is an open door.

Add a README warning: use `proxy` only when the app port is not reachable except through the authenticating proxy, for example by binding `127.0.0.1:2999:2999` in compose, and never use `none` on a reachable host. Code is not changed.

## Runnable tests

### T1. Clean-clone `go test ./...`

Today it fails with `internal/frontend/embed.go: pattern dist: no matching files`. CI works around this by creating a stub `dist/index.html`.

**Required behavior:**
- `go test ./...` and `go vet ./...` pass on a fresh clone with no built frontend.
- `make build` and the Docker build still embed the real SPA.
- The working tree stays clean after a local frontend build.
- CI drops the stub step.

**Suggested approach** (the builder may choose another that meets the behavior): `//go:embed all:dist` with a tracked `internal/frontend/dist/.gitkeep`, and `.gitignore` changed to ignore `internal/frontend/dist/*` except `.gitkeep`. The router already tolerates a missing `index.html` (`router.go:240`).

### T2. Frontend tests runnable and in CI

- `cd frontend && npm ci && npm test` must pass.
- Add a `test-frontend` CI job running `npm test`, and add it to `needs` of `build-and-push`, as the other test jobs are.
- The acceptance rule is that the suite is green. If an existing test fails:
  - Fix it when the cause is environment or setup, or a bug inside this project's scope.
  - When the cause is a real bug outside this scope, stop and ask the owner: fix it here, or move the CI job to a later project. Never skip, delete or weaken the test.

### T3. Tests added by this project

All tests use the existing conventions: Go `testing` with `internal/testutil`, and the existing handler and repository test patterns.

| Area | Tests |
|------|-------|
| `middleware/auth.go` | each mode: valid, missing and invalid credentials; JWT with wrong alg, no exp, expired, no `sub`, unknown `sub`; the OIDC state token rejected as a session; API key valid, wrong, and absent with a cookie fallback |
| OIDC callback | fake IdP via `httptest` (discovery, JWKS, token endpoint, RS256 ID token signed with `golang-jwt` and `crypto/rsa`, so no new dependency): the allowed email in the ID token links the user; the allowed email only in userinfo, with a matching `sub`, links the user; a userinfo `sub` mismatch gets 403; a different email gets 403; `email_verified` false or missing gets 403; missing `OIDC_ALLOWED_EMAIL` fails startup |
| Attachments | REST and sync reject non-link types; traversal names cannot be read or deleted through `os.Root`; script-scheme links are rejected; legitimate `https://` and `obsidian://` links still work; multipart upload, download and delete still work |
| Sync push | a wrong-typed field yields a per-change error and the rest of the batch applies; no panic |
| Router | a panicking handler returns the exact 500 JSON body; an oversized JSON body gets 413, including one sent with a `multipart/form-data` header to a JSON route; the upload route accepts a file above 10 MiB and below `MAX_UPLOAD_SIZE` |
| MCP compatibility | a link create without `type` (the MCP `add_link` shape) still succeeds |
| `writeError` | a 5xx body is generic and the message is logged; 4xx messages pass through unchanged |
| Login | the 11th failure from one IP within the window gets 429; another IP is unaffected; `X-Forwarded-For` is honored only from a loopback or private `RemoteAddr`; success before the limit works; the window expiry resets it (time injected) |
| Sync errors | a DB error in a change yields `internal error`; `unsupported entity type` text is unchanged |
| Cookies | `Secure` is set with TLS or `X-Forwarded-Proto: https`, and unset over plain HTTP |

## Docs to update

- `docs/api.md`: attachment create accepts only `link` over JSON; 400 for script-scheme URLs; 413 body limit; 429 on login; generic 500 body.
- `README.md` and `.env.example`: `OIDC_ALLOWED_EMAIL`; the proxy and none deployment warning.
- `CHANGELOG.md`: a security entry.

## Calls made (designer's, not the owner's)

- `OIDC_ALLOWED_EMAIL` is required in oidc mode. This breaks existing oidc configs on purpose: no safe automatic linking exists.
- OIDC requires `email_verified: true`. An IdP that omits the claim cannot be used.
- The login limiter is per client IP, with 10 failures per 15 minutes. `X-Forwarded-For` is trusted only from loopback or private peers.
- The OIDC userinfo fallback is kept, guarded by a `sub` match, so Authelia ≥ 4.39 keeps working.
- Web-UI links the server now rejects (`data:`, `file:`, etc.) stay stuck in the local sync queue. The stuck-queue bug is pre-existing (frontend audit H3) and is fixed in project 4. Such links are rare, so this degradation is accepted until then.
- The link-URL check is a scheme denylist, not an allowlist, so app links keep working.
- The JSON body limit is 10 MiB.
- `Secure` follows `X-Forwarded-Proto`. The app trusts that header for the cookie flag only.
- Tokens, CORS, input validation and wider test coverage are deferred to projects 2–4.

## Left to the builder

- The exact embed and gitignore mechanics for T1, as long as the required behavior holds.
- The order of the fixes.
- How to inject the clock into the login limiter.

## Verification

- `go test ./... -race` and `go vet ./...` from a clean clone.
- `golangci-lint run`.
- `cd frontend && npm ci && npm test && npm run lint`.
- Re-run the audit's attacks against a locally built server and confirm each fails:
  - the attachment traversal through REST and sync;
  - the replayed `oidc_state` cookie;
  - the wrong-type sync push.
