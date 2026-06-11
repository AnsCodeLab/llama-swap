# HuggingFace Model Downloads — Design

Date: 2026-06-11
Status: Approved

## Goal

Let users browse, search, download, and delete GGUF models from HuggingFace
directly in the llama-swap web UI (Models page). Downloaded models become
immediately usable: llama-swap auto-adds a config.yaml entry for them.

## Background

- Models exist in llama-swap only as config.yaml entries whose `cmd` points at
  a GGUF file path. There is no built-in models directory and no download code.
- The UI (ui-svelte) talks to the Go backend via REST endpoints under `/api/`
  and receives push updates over SSE at `/api/events`.
- Typical user setup: flat GGUF files in a single directory (e.g.
  `~/models`), referenced via a config macro.

## Decisions (from brainstorming)

1. **Auto-add config entry**: completing a download appends a model entry to
   config.yaml so the model is immediately loadable.
2. **`modelsDir` setting**: a new top-level config field names the download
   directory. If unset, the download feature is disabled and the UI explains
   how to enable it.
3. **Full management**: deleting from the UI removes both the GGUF file(s) and
   the config entry, including hand-written entries. Config edits must
   preserve comments and formatting.
4. **All-in-Go backend**: the Go server proxies the HF API, performs downloads
   itself, pushes progress over the existing SSE channel, and rewrites
   config.yaml. No browser→HF calls, no external CLI dependency.

## Config additions

```yaml
modelsDir: /home/user/models       # enables the feature; unset = disabled
hubToken: ${env.HF_TOKEN}          # optional, for gated/rate-limited repos
hubCmdTemplate: |                  # optional template for auto-added entries
  ${llama-server} --port ${PORT} -m ${MODEL_PATH}
```

- `hubCmdTemplate` default: `llama-server --port ${PORT} -m ${MODEL_PATH}`.
- `${MODEL_PATH}` is substituted with the downloaded file's absolute path when
  the entry is generated. Other `${...}` references are written verbatim so
  they continue to resolve as config macros at load time.

## Backend — new package `internal/hub`

### Endpoints (registered in `server.go` under the existing `apiChain`)

| Endpoint | Behavior |
|---|---|
| `GET /api/hub/popular` | Proxy `huggingface.co/api/models?filter=gguf&sort=downloads`, cached ~5 min |
| `GET /api/hub/search?q=` | Same API with `search=` term |
| `GET /api/hub/repo/{org}/{repo}` | List repo's GGUF files (name, size, quant); flag `downloaded: true` when a same-named file exists in `modelsDir` |
| `POST /api/hub/download` | Body `{repo, file}`. Starts server-side download; returns download id. Multi-part GGUFs (`-00001-of-0000N`) fetch all parts. Checks free disk space first. |
| `POST /api/hub/download/{id}/cancel` | Cancel an active download |
| `GET /api/hub/downloads` | Snapshot of active downloads (for page load) |
| `POST /api/hub/delete` | Body `{modelId}`. Unloads if running, deletes GGUF file(s), removes config entry |

Delete resolves a model's files from its macro-expanded `cmd` (`-m` and
`--mmproj` arguments) so hand-written entries work too. Files are only
deleted when they live inside `modelsDir`; files elsewhere are left on disk
(the config entry is still removed) and the response says so.

### Download mechanics

- GET `https://huggingface.co/{repo}/resolve/main/{file}` with
  `Authorization: Bearer <hubToken>` when set.
- Write to `<name>.gguf.part`; resume with HTTP Range on retry; rename to
  final name on completion. Failed downloads keep the `.part` for resume.
- Progress (bytes, total, speed, state) emitted on the internal event bus and
  forwarded over `/api/events` SSE as a new `downloadStatus` message type,
  throttled to ~2 updates/sec. Downloads run server-side, so they survive
  page reloads and closed browsers.

### Config rewriting

- Use yaml.v3 node API (comment-preserving) to add/remove entries under
  `models:`.
- Auto-generated entry id is derived from the filename, lowercased
  (e.g. `qwen3-4b-instruct-2507-ud-q4_k_xl`); collisions get a numeric suffix.
- Writes are atomic: temp file in the same directory + rename.
- After a write, config is reloaded in-process via the same path as the
  existing file-watcher reload (emit `ConfigFileChangedEvent`).

## UI — Models page (ui-svelte)

Two tabs on the Models page:

- **Installed** — the existing model table, plus:
  - a delete button per model with a confirm dialog
  - active download rows with progress bar, speed, and cancel button
- **Discover** — search box and popular-GGUF-repos list (name, downloads,
  likes). A repo expands to its GGUF files with size and quant label; each
  file shows a **Download** button or a **Downloaded ✓** badge when the file
  already exists in `modelsDir`.

Implementation follows existing patterns: writable stores and fetch helpers
in `stores/api.ts`, SSE subscription extended for `downloadStatus`, Svelte 5
runes as in `ModelsPanel.svelte`.

If `modelsDir` is unset, the Discover tab renders a hint describing the
config setting instead of the browse UI.

## Error handling

- HF API failures and download errors surface in the UI with retry.
- Delete refuses while a model is `starting`; `ready` models are unloaded
  before file removal.
- Disk-space check before starting a download; clear error if insufficient.
- Config write failures leave the original file untouched (atomic rename).

## Testing

- Go: `TestHubManager_...` unit tests with `httptest`-mocked HF API covering
  search proxy, download, resume, cancel, multi-part; config rewrite
  round-trip tests asserting comments/formatting survive add+remove.
- `make test-dev` after new tests; `make test-all` before completion.
- UI: `make test-ui` after Svelte changes.
