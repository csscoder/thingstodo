# Security Hardening and Runnable Test Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the audited security holes (S1–S10) and make the Go and frontend test suites run from a clean clone and in CI.

**Architecture:** Fixes go into the existing handler → repository → SQLite layers. New cross-cutting code is three small middlewares (`Recover`, `BodyLimit`, and the hardened `Auth`), plus a few helpers in `internal/handler/helpers.go`. Every fix is test-first (RED → GREEN), using Go `testing` and `internal/testutil`.

**Tech Stack:** Go 1.24, chi v5.2.5, golang-jwt v5.3.1, go-oidc v3.17.0, SQLite (modernc), React 19 + Vite 7 + Vitest 4.

**Spec:** `docs/superpowers/specs/2026-10-16-security-hardening-design.md`. Read it before starting; it holds the rationale and the sources.

## Global Constraints

- No new dependencies. Use only the stdlib (Go 1.24) and modules already in `go.mod`. `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2` are already required.
- TDD: each behavior change starts with a test that fails for the stated reason.
- Never skip, delete or weaken an existing test. If a test fails for a real bug outside this plan's scope, stop and ask the owner.
- JSON body limit: exactly `10 << 20` bytes (10 MiB).
- Login limiter: 10 failures per IP within 15 minutes, then 429 `RATE_LIMITED`.
- Generic 5xx body: exactly `{"error":"internal error","code":"INTERNAL"}`.
- Rejected link schemes, case-folded: `javascript`, `data`, `vbscript`, `file`.
- Any behavior change to an endpoint updates `docs/api.md` in the same task (project rule).
- Commit messages: a short `type: subject` line, no trailers.
- Run all Go commands from the repo root. Run frontend commands in `frontend/`.

## Review Focus

1. **The web UI keeps logging in.** A builtin-mode session cookie issued by `Login` must still pass the hardened `Auth`, since it has `sub` and `exp` and is HS256. Pinned by `TestAuth_Builtin_ValidSession` in Task 5.
2. **The MCP `add_link` shape keeps working.** It sends `{url, title}` with no `type`. Pinned by `TestCreateAttachment_LinkWithoutType` in Task 8.
3. **Sync of an ordinary offline queue is unaffected.** The new type checks must accept `null` for optional string fields, which the web UI sends to clear values. Pinned by `TestSyncPush_NullOptionalFieldsAccepted` in Task 3.
4. **File upload above 10 MiB still works.** The body limit must not cap the multipart route. Pinned by `TestBodyLimit_UploadRouteExempt` in Task 4.
5. **A clean clone with no built SPA still serves the API.** `/health` must answer when `dist/` holds only `.gitkeep`. The existing `TestHealthEndpoint` covers this in Task 1, and the clean-clone run verifies it.

---

### Task 1: Clean-clone `go test` and frontend tests in CI (spec T1, T2)

**Files:**
- Modify: `internal/frontend/embed.go`
- Create: `internal/frontend/dist/.gitkeep` (empty)
- Create: `frontend/public/.gitkeep` (empty). Vite's `emptyOutDir` wipes `dist/`, then `copyDir(publicDir, outDir)` copies dotfiles too (checked in `node_modules/vite/dist/node/chunks/config.js`), so the tracked `.gitkeep` survives builds.
- Modify: `.gitignore:12`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: `frontend.StaticFiles` embeds `all:dist`, with the same name and type as before.

- [ ] **Step 1: Confirm RED.**
  - First check: `ls internal/frontend/dist` must fail. If a local build created the directory, move it aside into a `mktemp -d` directory first, and restore it after this step.
  - Run: `go vet ./...`
  - Expected: FAIL, `pattern dist: no matching files found`.
- [ ] **Step 2: Change the embed directive and add the placeholders.**
  - Change the directive to `//go:embed all:dist`.
  - Create both `.gitkeep` files.
  - In `.gitignore`, replace `internal/frontend/dist/` with two lines: `internal/frontend/dist/*` and `!internal/frontend/dist/.gitkeep`.
- [ ] **Step 3: Update CI.**
  - In `.github/workflows/ci.yml`, delete the two `mkdir -p internal/frontend/dist && echo ...` steps.
  - Add a job `test-frontend`: checkout, `setup-node@v4` with node `"22"` and the npm cache on `frontend/package-lock.json`, then `cd frontend && npm ci`, then `cd frontend && npm test`.
  - Add `test-frontend` to `build-and-push.needs`.
- [ ] **Step 4: Verify GREEN locally.**
  - Run: `go vet ./... && go test ./... -race`. Expected: PASS for all packages, including `internal/router` and `cmd/server`.
  - Run: `cd frontend && npm test`. Expected: `71 passed` or more.
- [ ] **Step 5: Verify that a build keeps the tree clean.**
  - Run: `cd frontend && npm run build && cd .. && git status --short internal/frontend`.
  - Expected: no output, meaning `.gitkeep` is unchanged and the build output is ignored.
- [ ] **Step 6: Commit, then verify a clean clone.**
  - Commit: `git add internal/frontend/embed.go internal/frontend/dist/.gitkeep frontend/public/.gitkeep .gitignore .github/workflows/ci.yml && git commit -m "build: make go test pass without built frontend; run frontend tests in CI"`
  - Clean clone: `D=$(mktemp -d) && git clone -q "$PWD" "$D/c" && (cd "$D/c" && go vet ./... && go test ./...)`
  - Expected: PASS.

---

### Task 2: Generic 5xx bodies and sanitized sync errors (spec S5)

**Files:**
- Modify: `internal/handler/helpers.go`
- Modify: `internal/handler/sync.go`: the 25 `result.Error = err.Error()` sites and the 6 `result.Error = cErr.Error()` sites.
- Test: `internal/handler/helpers_test.go` (new, `package handler`)
- Test: `internal/handler/sync_test.go`

**Interfaces:**
- Produces: `writeError(w http.ResponseWriter, status int, msg, code string)`. The signature is unchanged. For `status >= 500` it logs `msg` with `log.Printf` and writes the generic body.
- Produces: `syncErrorMessage(err error) string`.
  - Returns `err.Error()` when `errors.Is(err, repository.ErrDuplicateProjectName | ErrDuplicateAreaName | ErrDuplicateTagName)`.
  - Otherwise it logs and returns `"internal error"`.

- [ ] **Step 1: Write failing tests.**
  - `TestWriteError_5xxIsGeneric`: `writeError(rec, 500, "sql: no such column x", "INTERNAL")` → the body equals the generic body and does not contain `"sql:"`.
  - `TestWriteError_4xxPassesThrough`: `writeError(rec, 400, "title is required", "VALIDATION")` → the body has `"error":"title is required"`.
  - `TestSyncErrorMessage`: a duplicate-name sentinel returns its own text; `fmt.Errorf("insert task: %w", errors.New("UNIQUE constraint"))` returns `"internal error"`.
  - `TestSyncPush_UnsupportedEntityMessageUnchanged`: pushing entity `"bogus"` yields `Error == "unsupported entity type: bogus"`.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/handler/ -run 'TestWriteError|TestSyncErrorMessage|TestSyncPush_Unsupported' -v`
  - Expected: `TestWriteError_5xxIsGeneric` and `TestSyncErrorMessage` FAIL. The other two pass and stay as guards.
- [ ] **Step 3: Implement.**
  - Change `writeError` as described above.
  - Add `syncErrorMessage` to `sync.go`.
  - Replace every `result.Error = err.Error()` and `result.Error = cErr.Error()` with `syncErrorMessage(err)` or `syncErrorMessage(cErr)`.
  - Leave literal validation strings as they are.
- [ ] **Step 4: Run the package.**
  - Run: `go test ./internal/handler/ -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.**
  - In `docs/api.md`, in the error-format section, state that 5xx responses carry the generic body.
  - Commit: `git add internal/handler docs/api.md && git commit -m "fix: stop leaking internal error text in 5xx and sync results"`

---

### Task 3: Recover middleware and checked type assertions in sync (spec S4)

**Files:**
- Create: `internal/middleware/recover.go`
- Modify: `internal/router/router.go:30`. Add `r.Use(mw.Recover)` before `r.Use(mw.Logger)`.
- Modify: `internal/handler/sync.go`, every unchecked `.(string)` on `change.Data` values: lines 350–371, 442–466, 601–609, 652–667, 796, 824–829, 882–886, 908–918 and 1090.
- Test: `internal/middleware/recover_test.go` (new)
- Test: `internal/handler/sync_test.go`

**Interfaces:**
- Produces: `func Recover(next http.Handler) http.Handler` in package `middleware`. It logs the panic value and `debug.Stack()`, then writes status 500 with the generic JSON body and `Content-Type: application/json`.
- Produces: `func optString(v interface{}) (*string, bool)` in `sync.go`. It returns `(nil, true)` for `nil`, `(&s, true)` for a string, and `(nil, false)` for any other type.
- Produces: `func fieldTypeError(result SyncPushResult, field string) SyncPushResult`. It sets `Status: "error"` and `Error: "invalid type for field " + field`.

- [ ] **Step 1: Write failing tests.**
  - `TestRecover_PanicReturnsJSON500`: a handler that panics, wrapped in `Recover` → status 500 and the exact generic body.
  - `TestSyncPush_WrongTypeIsPerChangeError`: one push with 3 changes:
    1. a task create with `"when_date": 123`;
    2. a valid task create;
    3. a task update with `fields:["project_id"]` and `"project_id": true`.
    - Expect HTTP 200, `results[0].Status=="error"` with `Error` containing `when_date`, `results[1].Status=="applied"` with the task existing, and `results[2].Status=="error"` with `Error` containing `project_id`.
  - `TestSyncPush_NullOptionalFieldsAccepted`: a task create and a task update with `"deadline": null` and `"project_id": null` → both `applied`.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/middleware/ ./internal/handler/ -run 'TestRecover|TestSyncPush_WrongType|TestSyncPush_NullOptional' -v`
  - Expected: `TestRecover` fails to compile (`undefined: middleware.Recover`). `TestSyncPush_WrongType` fails: the test server reports a panic or the connection is reset.
- [ ] **Step 3: Implement.**
  - Write `Recover` and mount it.
  - In `sync.go`, rewrite each unchecked site to the form `s, ok := optString(v); if !ok { return fieldTypeError(result, "<field>") }`, then assign `s` where the old code assigned `&s`.
  - Check: `grep -nE '\.\(string\)$' internal/handler/sync.go` must return nothing, and every `.(string)` left must be the `, ok` form.
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Commit.**
  - `git add internal/middleware/recover.go internal/middleware/recover_test.go internal/router/router.go internal/handler/sync.go internal/handler/sync_test.go && git commit -m "fix: recover from panics and reject wrong-typed sync fields per change"`

---

### Task 4: Request body limit (spec S6)

**Files:**
- Create: `internal/middleware/bodylimit.go`
- Modify: `internal/router/router.go`. Add `r.Use(mw.BodyLimit(mw.MaxJSONBody))` after `Recover` and `Logger`.
- Modify: `internal/handler/helpers.go`
- Modify: `internal/handler/attachments.go:52`, the JSON branch.
- Modify: every `decodeJSON` / `decodeJSONWithRaw` error branch in `internal/handler/*.go`, 32 sites, each currently `writeError(w, http.StatusBadRequest, "invalid JSON", "BAD_REQUEST")`.
- Test: `internal/middleware/bodylimit_test.go` (new)
- Test: `internal/router/router_test.go`

**Interfaces:**
- Produces: `const MaxJSONBody int64 = 10 << 20` in package `middleware`.
- Produces: `func BodyLimit(limit int64) func(http.Handler) http.Handler`.
  - It wraps `r.Body` in `http.MaxBytesReader` for every request.
  - It skips exactly `POST` requests whose path matches `^/api/tasks/[^/]+/attachments$`.
  - It ignores `Content-Type`.
- Produces: `func writeDecodeError(w http.ResponseWriter, err error)` in `helpers.go`.
  - On `*http.MaxBytesError` (`errors.As`) it writes 413 `{"error":"request body too large","code":"BODY_TOO_LARGE"}`.
  - Otherwise it writes 400 `invalid JSON` / `BAD_REQUEST`.
- Consumes: the generic body from Task 2 is not involved.

- [ ] **Step 1: Write failing tests.**
  - In `bodylimit_test.go`:
    - `TestBodyLimit_CapsJSONRegardlessOfContentType`: a handler that reads the whole body, behind `BodyLimit(16)`. A 17-byte body sent with `Content-Type: multipart/form-data` → the read returns `*http.MaxBytesError`.
    - `TestBodyLimit_UploadRouteExempt`: `POST /api/tasks/abc/attachments` with a 17-byte body → read fully, no error.
  - In `router_test.go`, set up as in `TestHealthEndpoint`. That setup uses proxy auth, so send `Remote-User: <id of a user created via UserRepository.Create>` on every `/api` request, and create a task first.
    - `TestRouter_OversizedJSONIs413`: `POST /api/tasks` with an 11 MiB JSON string field → 413 with `code == "BODY_TOO_LARGE"`.
    - `TestRouter_UploadAbove10MiB`: set `MaxUploadSize` to 12 MiB in cfg. A multipart upload of a file just over 10 MiB to an existing task → 201.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/middleware/ ./internal/router/ -run 'TestBodyLimit|TestRouter_Oversized|TestRouter_Upload' -v`
  - Expected: FAIL. `BodyLimit` is undefined, and the oversized request returns 201 or 400 instead of 413.
- [ ] **Step 3: Implement.**
  - Write `BodyLimit` and `writeDecodeError`, and mount the middleware.
  - Replace each of the 32 decode error branches with `writeDecodeError(w, err)`.
  - In `attachments.go`, the JSON branch of `Create` sets `r.Body = http.MaxBytesReader(w, r.Body, middleware.MaxJSONBody)` before decoding.
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.**
  - In `docs/api.md`, document 413 `BODY_TOO_LARGE` for JSON bodies over 10 MiB.
  - Commit: `git add internal docs/api.md && git commit -m "fix: cap JSON request bodies at 10 MiB"`

---

### Task 5: Harden JWT session validation (spec S2) and auth middleware coverage (spec T3)

**Files:**
- Modify: `internal/middleware/auth.go`
- Modify: `internal/router/router.go:104`, the `mw.Auth` call.
- Test: `internal/middleware/auth_test.go` (new)

**Interfaces:**
- Produces: `type UserExistsFunc func(id string) (bool, error)`.
- Produces: `func Auth(cfg config.Config, apiKeyUserLookup UserLookupFunc, userExists UserExistsFunc) func(http.Handler) http.Handler`.
  - The cookie path parses with `jwt.WithValidMethods([]string{"HS256"})` and `jwt.WithExpirationRequired()`.
  - It rejects a token whose `typ` claim equals `"oidc_state"`.
  - It requires a non-empty string `sub` for which `userExists` returns `true`.
  - Every rejection is 401 with the existing JSON error strings.
- The router passes `func(id string) (bool, error) { u, err := userRepo.GetByID(id); return u != nil, err }`.

- [ ] **Step 1: Write failing tests** in `auth_test.go`.
  - Use a stub next handler that echoes `UserIDKey`. Sign tokens with `jwt.NewWithClaims`, using secret `"s"`.
  - Builtin mode:
    - `TestAuth_Builtin_ValidSession`: HS256 with `sub=u1` and future `exp`, user exists → 200 with id `u1`.
    - `TestAuth_Builtin_NoCookie` → 401.
    - `TestAuth_Builtin_BadSignature` → 401.
    - `TestAuth_Builtin_WrongAlg`: an HS384 token signed with the same secret → 401.
    - `TestAuth_Builtin_NoExp` → 401.
    - `TestAuth_Builtin_Expired` → 401.
    - `TestAuth_Builtin_NoSub` → 401.
    - `TestAuth_Builtin_UnknownSub`: `userExists` returns false → 401.
    - `TestAuth_Builtin_OIDCStateTokenRejected`: claims `{"state":"x","typ":"oidc_state","exp":future}` → 401.
  - OIDC mode: `TestAuth_OIDCMode_UsesSameCookieRules`, a valid session gives 200 and a token without `sub` gives 401.
  - Proxy mode: `TestAuth_Proxy_HeaderSet` → 200 with the header value; `TestAuth_Proxy_HeaderMissing` → 401.
  - None mode: `TestAuth_None_UsesFirstUser` → 200.
  - API key:
    - `TestAuth_APIKey_Valid` → 200 with the lookup id.
    - `TestAuth_APIKey_Wrong` → 401 `invalid api key`, even with a valid cookie.
    - `TestAuth_APIKey_AbsentFallsBackToCookie`: no header and a valid cookie → 200.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/middleware/ -run TestAuth -v`
  - Expected: compile FAIL, because `Auth` takes 2 arguments. After adapting the call to the new signature, `WrongAlg`, `NoExp`, `NoSub`, `UnknownSub` and `OIDCStateTokenRejected` FAIL.
- [ ] **Step 3: Implement** the new `Auth` and update the router call.
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Commit.**
  - `git add internal/middleware/auth.go internal/middleware/auth_test.go internal/router/router.go && git commit -m "fix: require HS256, exp and an existing sub for session JWTs"`

---

### Task 6: OIDC allowlist, verified email and state token type (spec S3, S2)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/handler/oidc.go`
- Test: `internal/config/config_test.go` (new)
- Test: `internal/handler/oidc_test.go` (new, `package handler_test`)
- Modify: `README.md` (OIDC table), `.env.example`, `docs/api.md` (OIDC callback 403)

**Interfaces:**
- Produces: `Config.OIDCAllowedEmail string`, read from env `OIDC_ALLOWED_EMAIL`.
- Produces: `func (c Config) Validate() error`.
  - It returns an error for the existing JWT_SECRET rule.
  - It also returns an error when `AuthMode == "oidc"` and `OIDCAllowedEmail == ""`.
  - `Load` calls it and `log.Fatal`s on the error.
- Produces: `signState` adds the claim `"typ": "oidc_state"`. `verifyState` returns false unless `typ == "oidc_state"`.
- The callback behaves as follows:
  1. Read `email` and `email_verified *bool` from the ID token.
  2. If `email == ""`, call userinfo. On error, or when `userInfo.Subject != idToken.Subject`, return 403. Otherwise take both claims from userinfo.
  3. Require `strings.EqualFold(email, cfg.OIDCAllowedEmail)` and a non-nil, true `email_verified`, else 403 `{"error":"account not allowed","code":"FORBIDDEN"}`.
  4. Use `cfg.OIDCAllowedEmail` as the canonical username for lookup and linking. The existing link and create branches stay.

- [ ] **Step 1: Write failing tests.**
  - In `config_test.go`:
    - `TestValidate_OIDCRequiresAllowedEmail`: `Config{AuthMode:"oidc", JWTSecret:"s"}.Validate()` returns non-nil; it returns nil once `OIDCAllowedEmail` is set.
    - `TestValidate_BuiltinRequiresJWTSecret`.
  - In `oidc_test.go`, build a fake IdP with `httptest.NewServer`:
    - `/.well-known/openid-configuration` returns `issuer` (the server URL), `authorization_endpoint`, `token_endpoint`, `jwks_uri`, `userinfo_endpoint` and `id_token_signing_alg_values_supported: ["RS256"]`.
    - `/jwks` serves an RSA-2048 key generated with `crypto/rsa`, with `kid` `k1` and `n`/`e` base64url-encoded.
    - `/token` returns `access_token`, `token_type: Bearer`, and an `id_token` signed with `golang-jwt` `SigningMethodRS256`, header `kid=k1`, claims `iss`, `aud=client`, `sub`, `exp`, `iat` plus per-test claims.
    - `/userinfo` returns per-test JSON.
  - The helper `doOIDCLogin(t, idClaims, userinfo map[string]any) *http.Response` runs this sequence:
    1. GET `/api/auth/oidc/login` with a client that does not follow redirects, and read the `oidc_state` cookie and the `state` query from `Location`.
    2. GET `/api/auth/oidc/callback?state=..&code=c` with that cookie.
  - Test cases:
    - `TestOIDC_AllowedVerifiedEmailInIDToken` → 302 to `/inbox` and a `token` cookie set.
    - `TestOIDC_EmailOnlyInUserinfoWithMatchingSub` → 302.
    - `TestOIDC_UserinfoSubMismatch` → 403.
    - `TestOIDC_DifferentEmail` → 403, and the existing user's username is unchanged.
    - `TestOIDC_EmailVerifiedFalse` → 403.
    - `TestOIDC_EmailVerifiedMissing` → 403.
    - `TestOIDC_StateTokenWithoutTypRejected`: a cookie signed like the old `signState`, without `typ` → 400 `invalid state`.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/config/ ./internal/handler/ -run 'TestValidate|TestOIDC' -v`
  - Expected: FAIL. `Validate` and `OIDCAllowedEmail` are undefined. Once those compile, `DifferentEmail`, `UserinfoSubMismatch`, `EmailVerified*` and `StateTokenWithoutTyp` FAIL.
- [ ] **Step 3: Implement** `Validate`, the config field, the state `typ` claim and the callback rules above.
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.**
  - README OIDC table: add `OIDC_ALLOWED_EMAIL`, required, saying it is the only email allowed to log in and that the IdP must assert `email_verified: true`.
  - Replace the old "other OIDC accounts are rejected" wording with an accurate one.
  - `.env.example`: add a commented `OIDC_ALLOWED_EMAIL=`.
  - `docs/api.md`: document the callback's 403 `FORBIDDEN`.
  - Commit: `git add internal/config internal/handler/oidc.go internal/handler/oidc_test.go README.md .env.example docs/api.md && git commit -m "fix: restrict OIDC login to one verified allowed email"`

---

### Task 7: Secure cookies and login rate limit (spec S7, S8)

**Files:**
- Modify: `internal/handler/helpers.go`
- Create: `internal/handler/login_limiter.go`
- Modify: `internal/handler/auth.go`: `Login`, `Logout`, and the `NewAuthHandler` constructor.
- Modify: `internal/handler/oidc.go`: the three `http.SetCookie` calls.
- Test: `internal/handler/login_limiter_test.go` (new, `package handler`)
- Test: `internal/handler/auth_test.go` (new, `package handler_test`)

**Interfaces:**
- Produces: `func isSecureRequest(r *http.Request) bool`, which returns `r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")`. Every `token` and `oidc_state` cookie sets `Secure: isSecureRequest(r)`.
- Produces: `func clientIP(r *http.Request) string`.
  - Start from the host part of `RemoteAddr`.
  - If that IP `IsLoopback()` or `IsPrivate()` and `X-Forwarded-For` is non-empty, return the trimmed rightmost comma-separated entry instead.
- Produces: `type loginLimiter struct` with these members:
  - `func newLoginLimiter(max int, window time.Duration, now func() time.Time) *loginLimiter`
  - `func (l *loginLimiter) blocked(ip string) bool`
  - `func (l *loginLimiter) fail(ip string)`
  - `func (l *loginLimiter) reset(ip string)`
- Limiter behavior:
  - It uses a fixed window per IP: the first failure starts it, and the window ends at start + `window`.
  - `blocked` is true while `count >= max` and the window has not ended.
  - Expired entries are pruned on each `fail` call.
  - Mark it with `// ponytail: in-memory per-IP fixed window; resets on restart, IP rotation evades it. Move to a persistent or proxy-level limiter if attacks appear.`
- `NewAuthHandler` builds `newLoginLimiter(10, 15*time.Minute, time.Now)`. The signature is unchanged.
- `Login` flow:
  1. If `blocked(clientIP(r))`, return 429 `{"error":"too many failed login attempts","code":"RATE_LIMITED"}`.
  2. On an unknown user or a wrong password, call `fail`.
  3. On success, call `reset`.

- [ ] **Step 1: Write failing tests.**
  - In `login_limiter_test.go`, with an injected clock:
    - `TestLoginLimiter_BlocksAfterMax`: 10 `fail` calls make `blocked` true, and 9 leave it false.
    - `TestLoginLimiter_OtherIPUnaffected`.
    - `TestLoginLimiter_WindowExpiryUnblocks`: advance the clock 15m+1s, then `blocked` is false.
    - `TestLoginLimiter_ResetClears`.
    - `TestClientIP` cases:
      - `RemoteAddr 203.0.113.5:1` with XFF `1.2.3.4` → `203.0.113.5`
      - `RemoteAddr 127.0.0.1:1` with XFF `9.9.9.9, 1.2.3.4` → `1.2.3.4`
      - `RemoteAddr 172.18.0.2:1` with XFF `1.2.3.4` → `1.2.3.4`
      - `RemoteAddr 10.0.0.1:1` with no XFF → `10.0.0.1`
  - In `auth_test.go`, use a builtin-mode `AuthHandler` with a user created via `UserRepository.Create` and a bcrypt hash:
    - `TestLogin_EleventhFailureIs429`: 10 bad passwords give 401 each; the 11th attempt, even with the correct password, gives 429 `RATE_LIMITED`.
    - `TestLogin_SuccessBeforeLimit`: 3 failures, then the correct password → 200 with a `token` cookie.
    - `TestLogin_SecureCookieBehindTLSProxy`: with `X-Forwarded-Proto: https` the cookie has `Secure`; over plain HTTP it does not.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/handler/ -run 'TestLoginLimiter|TestClientIP|TestLogin_' -v`
  - Expected: compile FAIL on the undefined `newLoginLimiter` and `clientIP`. After stubbing them, `EleventhFailureIs429` and `SecureCookie` FAIL.
- [ ] **Step 3: Implement** the helpers, the limiter and the `Login` flow, and set `Secure` at all five `SetCookie` sites (2 in `auth.go`, 3 in `oidc.go`).
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.**
  - `docs/api.md`: document the 429 `RATE_LIMITED` on `POST /api/auth/login`.
  - Commit: `git add internal/handler docs/api.md && git commit -m "fix: rate-limit failed logins per IP and mark cookies Secure behind TLS"`

---

### Task 8: Attachments: links only over JSON and sync, safe file paths, URL scheme check (spec S1, S9)

**Files:**
- Modify: `internal/handler/attachments.go`: `Create` JSON branch, `Delete`, `Download`.
- Modify: `internal/handler/sync.go:1024-1053` (`applyAttachmentChange`, create).
- Test: `internal/handler/attachments_test.go` (new, `package handler_test`)
- Test: `internal/handler/sync_test.go`
- Modify: `docs/api.md` (attachments section)

**Interfaces:**
- Produces: `func normalizeLinkInput(in *model.CreateAttachmentInput) error` in `attachments.go`.
  - Set `in.Type = "link"` when it is empty, and return an error if it is any other non-`"link"` value.
  - Trim `in.URL` and return an error if it is empty.
  - `url.Parse` it and return an error on a parse error.
  - Return an error if `strings.ToLower(u.Scheme)` is one of `javascript`, `data`, `vbscript`, `file`.
  - Error messages: `"only link attachments can be created via JSON"`, `"url is required"`, `"invalid url"`, `"url scheme not allowed"`.
- REST `Create`, JSON branch: on an error, return 400 `VALIDATION` with the message. The multipart branch is unchanged.
- Sync create: on an error, return `Status:"error"` with the message.
- `Download` and `Delete` for file attachments use `os.OpenRoot(h.attachmentsPath)`, then `root.Open(att.URL)` or `root.Remove(att.URL)`, and close the root.
  - Download: an `OpenRoot` or `Open` error gives 404 `file not found on disk`.
  - Delete: the rows are deleted first, as now. A `root.Remove` error is ignored, as `os.Remove`'s error is today.

- [ ] **Step 1: Write failing tests.**
  - In `attachments_test.go`, use a router with the attachment and task handlers mounted, as `setupSyncRouter` does in `sync_test.go`, with `attachmentsPath = t.TempDir()/attachments` and a sentinel file `secret.db` in `t.TempDir()`.
    - `TestCreateAttachment_LinkWithoutType`: `{"url":"https://x.test","title":"x"}` → 201 with `type == "link"`.
    - `TestCreateAttachment_FileTypeRejectedOverJSON`: `{"type":"file","url":"../secret.db"}` → 400 `VALIDATION`.
    - `TestCreateAttachment_ScriptSchemesRejected`, table-driven over `javascript:alert(1)`, `JavaScript:alert(1)`, ` data:text/html,x`, `vbscript:x`, `file:///etc/passwd`, `"java\tscript:alert(1)"` and `"https://x.test/\nfoo"` → 400 for each.
    - `TestCreateAttachment_AppLinksAllowed`: `obsidian://open?vault=v` and `siyuan://blocks/1` → 201.
    - `TestDownloadAttachment_TraversalBlocked`: insert a row directly with `AttachmentRepository.Create(taskID, {Type:"file", URL:"../secret.db"})`. GET `/api/attachments/{id}/file` → 404, and the body does not contain the sentinel content.
    - `TestDeleteAttachment_TraversalDoesNotRemoveOutsideFile`: with the same row, DELETE → 204, and `secret.db` still exists.
    - `TestFileAttachment_UploadDownloadDelete`: a multipart upload gives 201; the download returns identical bytes; the delete gives 204 and removes the stored file.
  - In `sync_test.go`:
    - `TestSyncPush_AttachmentFileTypeRejected`: a create with `type:"file", url:"../secret.db"` → `Status=="error"` and no row.
    - `TestSyncPush_AttachmentScriptSchemeRejected` → error.
    - `TestSyncPush_AttachmentLinkApplied` → applied.
- [ ] **Step 2: Run them.**
  - Run: `go test ./internal/handler/ -run 'TestCreateAttachment|TestDownloadAttachment|TestDeleteAttachment|TestFileAttachment|TestSyncPush_Attachment' -v`
  - Expected: `FileTypeRejected`, `ScriptSchemes`, `Traversal*` and the sync rejections FAIL. `LinkWithoutType`, `AppLinksAllowed`, `UploadDownloadDelete` and `LinkApplied` pass as guards.
- [ ] **Step 3: Implement** `normalizeLinkInput`, wire it into REST and sync, and switch `Download` and `Delete` to `os.Root`.
- [ ] **Step 4: Run.**
  - Run: `go test ./internal/... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.**
  - `docs/api.md`: JSON create accepts only links (`type` optional, defaults to `link`); script schemes are rejected with 400 `VALIDATION`; files are uploaded only via multipart.
  - Commit: `git add internal/handler docs/api.md && git commit -m "fix: block attachment path traversal and script-scheme links"`

---

### Task 9: Deployment warning, changelog and end-to-end verification (spec S10, Verification)

**Files:**
- Modify: `README.md`, Proxy Auth section.
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the README warning.**
  - Under Proxy Auth, add: use `AUTH_MODE=proxy` only when the app port is reachable solely through the authenticating proxy, e.g. bind `127.0.0.1:2999:2999` in compose.
  - Next to it, add: never use `AUTH_MODE=none` on a reachable host.
- [ ] **Step 2: Add the CHANGELOG entry.**
  - Add `## [Unreleased]` with a `### Security` list, one line per fix S1–S9.
  - Mark the breaking item: `OIDC_ALLOWED_EMAIL` is now required in oidc mode.
- [ ] **Step 3: Run the full verification.**
  - Run: `go vet ./... && go test ./... -race && golangci-lint run`. Expected: PASS with no lint findings.
  - Run: `cd frontend && npm ci && npm test && npm run lint`. Expected: PASS.
- [ ] **Step 4: Replay the audit attacks against a local server.**
  - Start the server in a temp data dir:

    ```
    D=$(mktemp -d)
    DB_PATH=$D/thingstodo.db AUTH_MODE=builtin JWT_SECRET=s LOGIN_PASSWORD=p API_KEY=k go run ./cmd/server
    ```

  - Then, with `Authorization: Bearer k`:
    1. Create a task. POST `/api/tasks/{id}/attachments` with `{"type":"file","url":"../thingstodo.db"}` → 400.
    2. POST `/api/sync/push` with the same attachment create → result `error`.
    3. POST `/api/sync/push` with a task create carrying `"when_date":123` → 200 with a per-change `error`, and the server keeps running.
    4. Forge an HS256 token `{"state":"x","typ":"oidc_state","exp":future}` with secret `s` and send it as `Cookie: token=...` to `/api/tasks` with no bearer header → 401.
  - Expected: every attack fails as described. Record the outputs in the final report.
- [ ] **Step 5: Commit.**
  - `git add README.md CHANGELOG.md && git commit -m "docs: security changelog and proxy/none deployment warning"`
