# Username/Password Protection + API Key Management — Design

Date: 2026-07-06
Status: Approved

## Goal

Today, `apiKeys` in `config.yaml` gates only inference/API routes — the `/ui/`
dashboard, `/metrics`, `/health`, etc. have no auth at all, even when
`apiKeys` is configured. Add:

1. A username/password gate that protects literally every route llama-swap
   serves.
2. A Settings page in the UI to configure that username/password and to
   generate/label/revoke API keys at runtime, instead of hand-editing YAML.

## Decisions

- **HTTP Basic Auth**, not a session/cookie login page. No session
  infrastructure exists today; Basic Auth works uniformly for both browsers
  and API/SDK clients and reuses the existing `Authorization` header
  handling.
- **Alternate credentials, not stacked layers**: a request is admitted if it
  presents *either* a valid API key (existing mechanism, unchanged) *or*
  valid username/password. Existing `apiKeys`-based integrations keep working
  unmodified.
- **Plaintext storage** for username/password in `config.yaml`, consistent
  with how `apiKeys` are stored today, including `${env.VAR}` macro support.
- **API keys gain metadata** (`label`, `createdAt`) instead of staying bare
  strings, so the Settings UI can show human-readable, manageable entries.
  Bare-string entries in existing configs continue to work unchanged.
- **Reuse and share the config-file-editing mutex.** `internal/hub/configedit.go`
  already has a mutex-protected read-modify-write-encode-atomic-write cycle
  for `config.yaml` (added after a real data-loss bug from two independent
  unguarded writers — see commit `9fe5619`). The new Settings-driven writes
  (auth credentials, API key add/remove) must go through that same shared
  mutex and atomic-write path, not a second independent one, or the same class
  of race reappears. The generic parts (mutex, atomic write, root-mapping
  lookup, node helpers) move out of `internal/hub` into a shared location;
  `hub`'s model-entry editing becomes a thin wrapper over the shared editor.

## Config schema

```yaml
auth:
  username: "admin"
  password: "hunter2"       # plaintext; ${env.VAR} macros supported

apiKeys:
  - key: "sk-...."
    label: "CI pipeline"     # optional
    createdAt: "2026-07-06T10:00:00Z"
  - "sk-legacy-bare-string"  # still accepted, treated as {key: "sk-legacy-bare-string"}
```

`internal/config/config.go`:

- New `AuthConfig struct { Username, Password string }`, field `Auth AuthConfig \`yaml:"auth"\`` on `Config`. Empty username *and* empty password (the default) means auth is disabled, matching `apiKeys`' existing default-allow behavior.
- `RequiredAPIKeys` changes from `[]string` to `[]APIKeyEntry` where
  `APIKeyEntry struct { Key, Label, CreatedAt string }`, with a custom
  `UnmarshalYAML` on `APIKeyEntry` that accepts either a scalar string node
  (legacy — becomes `{Key: value}`) or a mapping node (decoded normally).
  Existing validation (non-empty, no spaces) applies to `.Key`.
- `docs/configuration.md` and `config-schema.json` updated for both new
  shapes.

## Shared config-edit infra

Move `internal/hub/configedit.go`'s generic pieces (`configEditMu`,
`editConfig`, `atomicWrite`, `strNode`, `errNoChange`, root-mapping lookup)
into `internal/config/edit.go`. `hub.AddModelEntry`/`RemoveModelEntry` become
thin callers of `config.EditConfig(...)`. New functions in the same package:

- `AddAPIKey(configPath, label string) (plaintextKey string, err error)` —
  generates `"sk-" + base64url(32 random bytes via crypto/rand)`, a separate
  non-secret `id` (12 hex chars, also `crypto/rand`), appends
  `{key, label, createdAt: now}` under `apiKeys:`, returns the plaintext key
  once.
- `RemoveAPIKey(configPath, id string) (bool, error)` — removes by `id`, not
  by key value, so the secret never has to appear in a request path/URL or
  server access log.
- `SetAuthCredentials(configPath, username, password string) error` — writes
  (or clears, if both empty) the top-level `auth:` mapping.

All three go through the one shared mutex/atomic-write path used by the hub
feature; the existing config-file watcher picks up the change and hot-reloads
the server automatically — no new reload mechanism needed.

## Backend auth middleware

`internal/server/auth.go`: replace the current `CreateAuthMiddleware` (only
wired into `modelChain`/`apiChain`) with a middleware applied at the
outermost layer:

```go
s.handler = chain.New(
    CreateRequestLogMiddleware(s.proxylog),
    CreateCORSMiddleware(),   // must run first: answers OPTIONS before auth
    CreateGlobalAuthMiddleware(s.cfg),
).Then(mux)
```

`CreateGlobalAuthMiddleware(cfg)` logic per request:

1. If `cfg.Auth.Username == "" && cfg.Auth.Password == "" && len(cfg.RequiredAPIKeys) == 0` → pass-through (today's default-allow).
2. Else if `extractAPIKey(r)` matches any configured key → allow (existing
   Bearer/Basic-password-field/x-api-key extraction, unchanged).
3. Else if `r.BasicAuth()` succeeds and matches `cfg.Auth.Username`/`Password`
   via `subtle.ConstantTimeCompare` → allow.
4. Else → `401` + `WWW-Authenticate: Basic realm="llama-swap"`.

This now covers `/ui/`, `/metrics`, `/health`, `/favicon.ico`, `/`, and every
other route — the per-chain `authMW` applications in `routes()` are removed
since the global middleware supersedes them.

## API endpoints (`internal/server/settingsapi.go`, new)

All under the (now-global) auth gate, mirroring the existing `/api/hub/*`
pattern:

- `GET /api/settings/auth` → `{ "enabled": bool, "username": string }` (never
  the password).
- `PUT /api/settings/auth` → body `{ username, password }`; both empty
  disables protection. Calls `config.SetAuthCredentials`.
- `GET /api/settings/apikeys` → `[{ id, label, createdAt, maskedKey }]`,
  `maskedKey` like `sk-ab12…wxyz`.
- `POST /api/settings/apikeys` → body `{ label? }`; calls `config.AddAPIKey`,
  returns `{ id, label, createdAt, key }` — full `key` only in this one
  response.
- `DELETE /api/settings/apikeys/{id}` → calls `config.RemoveAPIKey`.

## UI

- New route `/settings` (`ui-svelte/src/routes/Settings.svelte`), nav link in
  `Header.svelte`, alongside Playground/Models/Activity/Logs/Performance.
- New `ui-svelte/src/lib/settingsApi.ts` following the existing `lib/*Api.ts`
  pattern (e.g. `chatApi.ts`).
- **Access Control** card: shows current protection status; form with
  username + new password + confirm password; "Save" persists via
  `PUT /api/settings/auth`; clearing both fields and saving disables
  protection behind a confirm dialog ("Anyone will be able to access this
  server. Continue?").
- **API Keys** card: table of `label / createdAt / maskedKey / [Delete]`;
  "+ Generate key" opens a small dialog for an optional label, then shows the
  returned plaintext key once in a copy-to-clipboard box with a "won't be
  shown again" warning.

## Testing

- Go: `internal/server/auth_test.go` — API-key-valid, Basic-Auth-valid, both
  invalid, both unset (pass-through), OPTIONS preflight bypass,
  constant-time comparison used for password check.
- Go: `internal/config` — back-compat parsing (bare-string vs struct
  `apiKeys` entries), `AddAPIKey`/`RemoveAPIKey`/`SetAuthCredentials`
  round-trips, concurrent-edit test analogous to the existing hub
  regression test (two concurrent API-key adds don't drop each other's
  entries or pre-existing model entries).
- UI: `settingsApi` and `Settings.svelte` tests per existing `npm test`
  conventions.
- `make test-dev` (proxy/config changes) and `make test-ui` before wrapping
  up; `make test-all` before merging.
