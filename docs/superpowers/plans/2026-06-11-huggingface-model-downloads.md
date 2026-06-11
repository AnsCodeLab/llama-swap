# HuggingFace Model Downloads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Browse, search, download, and delete GGUF models from HuggingFace in the llama-swap web UI; completed downloads are auto-added to config.yaml.

**Architecture:** A new `internal/hub` package holds an HF API client, a server-side download manager (resume, multi-part, progress over the existing SSE channel), and comment-preserving config.yaml editing. The manager is created once in `main` (it must survive config reloads, like `perfMon`) and injected into each `server.Server` via a setter. New `/api/hub/*` REST endpoints serve the Svelte UI, where the Models page gains Installed/Discover tabs.

**Tech Stack:** Go (net/http, gopkg.in/yaml.v3 node API, gopsutil for disk space), Svelte 5 + TypeScript.

**Key existing facts (verified):**
- Routes register in `internal/server/server.go` `routes()` (~line 234) as `mux.Handle("POST /api/...", apiChain.ThenFunc(...))`.
- SSE pump is `handleAPIEvents` in `internal/server/apigroup.go`; envelope `{type, data}` with `messageType` consts at line ~177.
- Event bus: `event.On(func(e shared.XEvent){...})()` / `event.Emit(ev)`; event types live in `internal/shared/events.go` with IDs 0x01,0x03,0x05,0x06,0x07 used (use 0x08).
- `config.Config` is in `internal/config/config.go`; `LoadConfigFromReader` applies defaults (~line 239) and validation. `config.SanitizeCommand` splits a cmd string into argv.
- `Server` has `s.cfg config.Config`, `s.local router.LocalRouter` with `RunningModels() map[string]<state>` and `Unload(timeout, models...)`; `apiUnloadTimeout` const in `internal/server/api.go`.
- `main` (`llama-swap.go`) owns `configPath` and `reload()`; reload builds a new `server.New(...)` and swaps it.
- UI: stores in `ui-svelte/src/stores/api.ts` (SSE switch at line ~69), types in `ui-svelte/src/lib/types.ts`, Models page is `ui-svelte/src/routes/Models.svelte` wrapping `components/ModelsPanel.svelte`.
- Tests: `go test -v -run <pattern> ./internal/...`, `make test-dev`, `make test-all`, `make test-ui` (npm ci + svelte-check + vitest). `gofmt -w` before commits. Build binaries into `./build/`.

---

### Task 1: Config settings (`modelsDir`, `hubToken`, `hubCmdTemplate`)

**Files:**
- Modify: `internal/config/config.go` (Config struct ~line 120, LoadConfigFromReader defaults ~line 239)
- Test: `internal/config/config_hub_test.go` (new)
- Modify: `config.example.yaml` (document the new settings)

- [x] **Step 1: Write the failing test**

```go
package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_HubSettings(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
modelsDir: /tmp/models
hubToken: abc123
hubCmdTemplate: "${llama-server} --port ${PORT} -m ${MODEL_PATH}"
macros:
  llama-server: /usr/bin/llama-server
models: {}
`))
	require.NoError(t, err)
	assert.Equal(t, "/tmp/models", cfg.ModelsDir)
	assert.Equal(t, "abc123", cfg.HubToken)
	assert.Equal(t, "${llama-server} --port ${PORT} -m ${MODEL_PATH}", cfg.HubCmdTemplate)
}

func TestConfig_HubSettingsDefaults(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`models: {}`))
	require.NoError(t, err)
	assert.Equal(t, "", cfg.ModelsDir)
	assert.Equal(t, "llama-server --port ${PORT} -m ${MODEL_PATH}", cfg.HubCmdTemplate)
}

func TestConfig_HubModelsDirMustBeAbsolute(t *testing.T) {
	_, err := LoadConfigFromReader(strings.NewReader(`
modelsDir: relative/path
models: {}
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "modelsDir")
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -v -run 'TestConfig_Hub' ./internal/config/`
Expected: FAIL (unknown fields `cfg.ModelsDir` etc. — compile error)

- [x] **Step 3: Implement**

In the `Config` struct (after the `Peers` field):

```go
	// HuggingFace hub downloads (UI feature). ModelsDir enables it.
	ModelsDir      string `yaml:"modelsDir"`
	HubToken       string `yaml:"hubToken"`
	HubCmdTemplate string `yaml:"hubCmdTemplate"`
```

In `LoadConfigFromReader`, after the `logToStdout` validation switch:

```go
	if config.HubCmdTemplate == "" {
		config.HubCmdTemplate = "llama-server --port ${PORT} -m ${MODEL_PATH}"
	}
	if config.ModelsDir != "" && !filepath.IsAbs(config.ModelsDir) {
		return Config{}, fmt.Errorf("modelsDir must be an absolute path, got %q", config.ModelsDir)
	}
```

Add `"path/filepath"` to imports.

- [x] **Step 4: Run test to verify it passes**

Run: `go test -v -run 'TestConfig_Hub' ./internal/config/`
Expected: PASS (3 tests)

- [x] **Step 5: Document in config.example.yaml**

Add near the top-level settings section:

```yaml
# modelsDir: enables downloading GGUF models from HuggingFace via the web UI.
#   - must be an absolute path; downloaded .gguf files are stored here
#   - default: unset (the Discover tab in the UI is disabled)
#modelsDir: /path/to/models

# hubToken: optional HuggingFace token for gated or rate-limited repos.
#   - supports environment variables, e.g. ${env.HF_TOKEN}
#hubToken: ${env.HF_TOKEN}

# hubCmdTemplate: cmd used for config entries that are auto-added after a
# download completes. ${MODEL_PATH} is replaced with the downloaded file path;
# other macros are written as-is and resolve normally on config load.
#   - default: llama-server --port ${PORT} -m ${MODEL_PATH}
#hubCmdTemplate: "${llama-server} --port ${PORT} -m ${MODEL_PATH}"
```

- [x] **Step 6: Commit**

```bash
gofmt -w internal/config/config.go internal/config/config_hub_test.go
git add internal/config/ config.example.yaml
git commit -m "config: add modelsDir, hubToken, hubCmdTemplate settings"
```

---

### Task 2: DownloadStatusEvent in internal/shared

**Files:**
- Modify: `internal/shared/events.go`

- [x] **Step 1: Implement (leaf type, no behavior — no test needed beyond compile)**

Append to `internal/shared/events.go`:

```go
const DownloadStatusEventID = 0x08

// DownloadInfo is the status of one hub download job (may span multiple
// files for multi-part GGUFs). JSON tags match the UI's DownloadInfo type.
type DownloadInfo struct {
	ID              string  `json:"id"`
	Repo            string  `json:"repo"`
	File            string  `json:"file"`
	ModelID         string  `json:"modelId"`
	State           string  `json:"state"` // downloading | completed | error | cancelled
	TotalBytes      int64   `json:"totalBytes"`
	DownloadedBytes int64   `json:"downloadedBytes"`
	SpeedBps        float64 `json:"speedBps"`
	Error           string  `json:"error,omitempty"`
}

// DownloadStatusEvent carries a full snapshot of all download jobs.
type DownloadStatusEvent struct {
	Downloads []DownloadInfo
}

func (e DownloadStatusEvent) Type() uint32 {
	return DownloadStatusEventID
}
```

- [x] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success

- [x] **Step 3: Commit**

```bash
gofmt -w internal/shared/events.go
git add internal/shared/events.go
git commit -m "shared: add DownloadStatusEvent for hub downloads"
```

---

### Task 3: Comment-preserving config.yaml editing (`internal/hub/configedit.go`)

**Files:**
- Create: `internal/hub/configedit.go`
- Test: `internal/hub/configedit_test.go`

- [x] **Step 1: Write the failing tests**

```go
package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConfig = `# my llama-swap config
healthCheckTimeout: 600

macros:
  llama-server: /usr/bin/llama-server # path to llama-server

models:
  # hand-written entry, comments must survive edits
  "qwen3-4b":
    name: "Qwen3 4B"
    cmd: |
      ${llama-server} --port ${PORT} -m /models/qwen3-4b.gguf
    ttl: 600
`

func writeTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testConfig), 0644))
	return path
}

func TestConfigEdit_AddModelEntry(t *testing.T) {
	path := writeTestConfig(t)
	err := AddModelEntry(path, "new-model", "New Model", "llama-server --port ${PORT} -m /models/new-model.gguf")
	require.NoError(t, err)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, "# my llama-swap config")
	assert.Contains(t, s, "# hand-written entry, comments must survive edits")
	assert.Contains(t, s, "# path to llama-server")
	assert.Contains(t, s, `"new-model":`)
	assert.Contains(t, s, "-m /models/new-model.gguf")
	assert.Contains(t, s, `name: New Model`)
}

func TestConfigEdit_AddModelEntryNoModelsSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("healthCheckTimeout: 600\n"), 0644))
	err := AddModelEntry(path, "m1", "", "llama-server --port ${PORT} -m /models/m1.gguf")
	require.NoError(t, err)
	out, _ := os.ReadFile(path)
	assert.Contains(t, string(out), `"m1":`)
}

func TestConfigEdit_RemoveModelEntry(t *testing.T) {
	path := writeTestConfig(t)
	found, err := RemoveModelEntry(path, "qwen3-4b")
	require.NoError(t, err)
	assert.True(t, found)
	out, _ := os.ReadFile(path)
	s := string(out)
	assert.NotContains(t, s, "qwen3-4b")
	assert.Contains(t, s, "# my llama-swap config")
}

func TestConfigEdit_RemoveModelEntryNotFound(t *testing.T) {
	path := writeTestConfig(t)
	found, err := RemoveModelEntry(path, "nope")
	require.NoError(t, err)
	assert.False(t, found)
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test -v -run 'TestConfigEdit' ./internal/hub/`
Expected: FAIL (package does not exist)

- [x] **Step 3: Implement `internal/hub/configedit.go`**

```go
// Package hub implements HuggingFace model browsing, downloading, and
// config.yaml management for the web UI.
package hub

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AddModelEntry appends a model entry under the top-level models: mapping,
// preserving comments and formatting elsewhere in the file. The cmd is
// written as a literal block scalar.
func AddModelEntry(configPath, modelID, name, cmd string) error {
	return editConfig(configPath, func(models *yaml.Node) error {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if name != "" {
			entry.Content = append(entry.Content,
				strNode("name", 0), strNode(name, 0))
		}
		entry.Content = append(entry.Content,
			strNode("cmd", 0), strNode(cmd+"\n", yaml.LiteralStyle))

		models.Content = append(models.Content,
			strNode(modelID, yaml.DoubleQuotedStyle), entry)
		return nil
	})
}

// RemoveModelEntry deletes a model entry by ID. Returns false if not found.
func RemoveModelEntry(configPath, modelID string) (bool, error) {
	found := false
	err := editConfig(configPath, func(models *yaml.Node) error {
		for i := 0; i+1 < len(models.Content); i += 2 {
			if models.Content[i].Value == modelID {
				models.Content = append(models.Content[:i], models.Content[i+2:]...)
				found = true
				return nil
			}
		}
		return errNoChange
	})
	if err == errNoChange {
		return false, nil
	}
	return found, err
}

var errNoChange = fmt.Errorf("no change")

func strNode(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

// editConfig loads the raw YAML document, locates (or creates) the models:
// mapping, applies fn, and writes the document back atomically.
func editConfig(configPath string, fn func(models *yaml.Node) error) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a YAML mapping at the top level", configPath)
	}
	root := doc.Content[0]

	var models *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "models" {
			models = root.Content[i+1]
			break
		}
	}
	if models == nil {
		models = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, strNode("models", 0), models)
	}
	if models.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: models is not a mapping", configPath)
	}

	if err := fn(models); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encoding %s: %w", configPath, err)
	}
	enc.Close()

	return atomicWrite(configPath, buf.Bytes())
}

// atomicWrite replaces path with data via a temp file + rename in the same
// directory, preserving the original file mode.
func atomicWrite(path string, data []byte) error {
	mode := os.FileMode(0644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -v -run 'TestConfigEdit' ./internal/hub/`
Expected: PASS (4 tests)

- [x] **Step 5: Commit**

```bash
gofmt -w internal/hub/
git add internal/hub/
git commit -m "hub: add comment-preserving config.yaml model entry editing"
```

---

### Task 4: HF API client + filename helpers (`internal/hub/hf.go`)

**Files:**
- Create: `internal/hub/hf.go`
- Test: `internal/hub/hf_test.go`

- [x] **Step 1: Write the failing tests**

```go
package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubManager_Search(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/models", r.URL.Path)
		assert.Equal(t, "gguf", r.URL.Query().Get("filter"))
		assert.Equal(t, "qwen", r.URL.Query().Get("search"))
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		w.Write([]byte(`[{"id":"bartowski/Qwen-GGUF","downloads":1000,"likes":42}]`))
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL})
	repos, err := m.Search(context.Background(), "tok", "qwen", 30)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "bartowski/Qwen-GGUF", repos[0].ID)
	assert.Equal(t, int64(1000), repos[0].Downloads)
	assert.Equal(t, int64(42), repos[0].Likes)
}

func TestHubManager_ListFiles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/models/org/repo", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("blobs"))
		w.Write([]byte(`{"siblings":[
			{"rfilename":"README.md","size":10},
			{"rfilename":"model-Q4_K_M.gguf","size":1234},
			{"rfilename":"model-F16.gguf","size":5678}
		]}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model-Q4_K_M.gguf"), []byte("x"), 0644))

	m := NewManager(Options{BaseURL: ts.URL})
	files, err := m.ListFiles(context.Background(), "", "org/repo", dir)
	require.NoError(t, err)
	require.Len(t, files, 2) // README.md filtered out
	assert.Equal(t, "model-Q4_K_M.gguf", files[0].Name)
	assert.Equal(t, "Q4_K_M", files[0].Quant)
	assert.True(t, files[0].Downloaded)
	assert.Equal(t, "F16", files[1].Quant)
	assert.False(t, files[1].Downloaded)
}

func TestHubManager_DeriveModelID(t *testing.T) {
	assert.Equal(t, "qwen3-4b-instruct-2507-ud-q4_k_xl", DeriveModelID("Qwen3-4B-Instruct-2507-UD-Q4_K_XL.gguf"))
	assert.Equal(t, "big-model-q8_0", DeriveModelID("Big-Model-Q8_0-00001-of-00003.gguf"))
}

func TestHubManager_PartSet(t *testing.T) {
	files := []RepoFile{
		{Name: "solo-Q4.gguf", Size: 1},
		{Name: "big-Q8-00001-of-00002.gguf", Size: 2},
		{Name: "big-Q8-00002-of-00002.gguf", Size: 3},
	}
	parts := PartSet(files, "big-Q8-00002-of-00002.gguf")
	require.Len(t, parts, 2)
	assert.Equal(t, "big-Q8-00001-of-00002.gguf", parts[0].Name)

	solo := PartSet(files, "solo-Q4.gguf")
	require.Len(t, solo, 1)

	assert.Empty(t, PartSet(files, "missing.gguf"))
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test -v -run 'TestHubManager_(Search|ListFiles|DeriveModelID|PartSet)' ./internal/hub/`
Expected: FAIL (NewManager, Options, etc. undefined)

- [x] **Step 3: Implement `internal/hub/hf.go`**

```go
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const DefaultBaseURL = "https://huggingface.co"

// RepoSummary is one HF repo in popular/search results.
type RepoSummary struct {
	ID        string `json:"id"`
	Downloads int64  `json:"downloads"`
	Likes     int64  `json:"likes"`
}

// RepoFile is one GGUF file inside an HF repo.
type RepoFile struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Quant      string `json:"quant"`
	Downloaded bool   `json:"downloaded"`
}

var (
	quantRe      = regexp.MustCompile(`(?i)[-._](i?q\d[a-z0-9_]*|f16|bf16|f32)(?:[-._]|$)`)
	partSuffixRe = regexp.MustCompile(`-(\d{5})-of-(\d{5})$`)
)

// DeriveModelID converts a GGUF filename into a config model ID: strips the
// .gguf extension and any -NNNNN-of-NNNNN part suffix, lowercased.
func DeriveModelID(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), ".gguf")
	base = partSuffixRe.ReplaceAllString(base, "")
	return strings.ToLower(base)
}

// PartSet returns every file belonging to the same multi-part series as
// selected (or just the selected file when it is not multi-part), sorted by
// name so part 00001 comes first. Empty if selected is not in files.
func PartSet(files []RepoFile, selected string) []RepoFile {
	var sel *RepoFile
	for i := range files {
		if files[i].Name == selected {
			sel = &files[i]
			break
		}
	}
	if sel == nil {
		return nil
	}
	base := strings.TrimSuffix(sel.Name, ".gguf")
	m := partSuffixRe.FindStringSubmatch(base)
	if m == nil {
		return []RepoFile{*sel}
	}
	prefix := strings.TrimSuffix(base, m[0])
	var parts []RepoFile
	for _, f := range files {
		fb := strings.TrimSuffix(f.Name, ".gguf")
		if fm := partSuffixRe.FindStringSubmatch(fb); fm != nil && strings.TrimSuffix(fb, fm[0]) == prefix {
			parts = append(parts, f)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Name < parts[j].Name })
	return parts
}

func quantFromFilename(name string) string {
	if m := quantRe.FindStringSubmatch(strings.TrimSuffix(name, ".gguf")); m != nil {
		return m[1]
	}
	return ""
}

func (m *Manager) hfGet(ctx context.Context, token, path string, query url.Values, out any) error {
	u := m.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("huggingface API returned %s for %s", resp.Status, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Search queries the HF model index for GGUF repos sorted by downloads.
// An empty query returns the most popular repos.
func (m *Manager) Search(ctx context.Context, token, query string, limit int) ([]RepoSummary, error) {
	q := url.Values{}
	q.Set("filter", "gguf")
	q.Set("sort", "downloads")
	q.Set("direction", "-1")
	q.Set("limit", fmt.Sprintf("%d", limit))
	if query != "" {
		q.Set("search", query)
	}
	var repos []RepoSummary
	if err := m.hfGet(ctx, token, "/api/models", q, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// ListFiles returns the GGUF files in an HF repo. When modelsDir is non-empty
// each file is flagged Downloaded if a same-named file already exists there.
func (m *Manager) ListFiles(ctx context.Context, token, repo, modelsDir string) ([]RepoFile, error) {
	var info struct {
		Siblings []struct {
			Rfilename string `json:"rfilename"`
			Size      int64  `json:"size"`
		} `json:"siblings"`
	}
	q := url.Values{}
	q.Set("blobs", "true")
	if err := m.hfGet(ctx, token, "/api/models/"+repo, q, &info); err != nil {
		return nil, err
	}
	var files []RepoFile
	for _, s := range info.Siblings {
		if !strings.HasSuffix(s.Rfilename, ".gguf") {
			continue
		}
		downloaded := false
		if modelsDir != "" {
			if _, err := os.Stat(filepath.Join(modelsDir, filepath.Base(s.Rfilename))); err == nil {
				downloaded = true
			}
		}
		files = append(files, RepoFile{
			Name:       filepath.Base(s.Rfilename),
			Size:       s.Size,
			Quant:      quantFromFilename(s.Rfilename),
			Downloaded: downloaded,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}
```

Also create the Manager skeleton this file depends on, in `internal/hub/manager.go` (fleshed out in Task 5):

```go
package hub

import (
	"net/http"
	"sync"

	"github.com/mostlygeek/llama-swap/internal/shared"
)

// Options configures a Manager.
type Options struct {
	BaseURL    string // HF base URL, default DefaultBaseURL (override in tests)
	ConfigPath string // path to config.yaml for entry add/remove
	Logger     Logger // optional
}

// Logger is the subset of logmon.Monitor the hub needs.
type Logger interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
}

// Manager owns HF API access and active download jobs. Create it once in
// main: it must survive config reloads so downloads keep running.
type Manager struct {
	baseURL    string
	configPath string
	httpClient *http.Client
	logger     Logger
	reloadFn   func()

	mu        sync.Mutex
	downloads map[string]*download
}

type download struct {
	info   shared.DownloadInfo
	cancel func()
}

func NewManager(opts Options) *Manager {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	return &Manager{
		baseURL:    opts.BaseURL,
		configPath: opts.ConfigPath,
		httpClient: &http.Client{},
		logger:     opts.Logger,
		downloads:  make(map[string]*download),
	}
}

// SetReloadFunc registers the callback used to reload llama-swap's config
// after this manager edits config.yaml. Called asynchronously.
func (m *Manager) SetReloadFunc(fn func()) {
	m.reloadFn = fn
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -v -run 'TestHubManager_(Search|ListFiles|DeriveModelID|PartSet)' ./internal/hub/`
Expected: PASS (4 tests)

- [x] **Step 5: Commit**

```bash
gofmt -w internal/hub/
git add internal/hub/
git commit -m "hub: add HuggingFace API client and filename helpers"
```

---

### Task 5: Download manager (`internal/hub/manager.go`)

**Files:**
- Modify: `internal/hub/manager.go`
- Create: `internal/hub/download.go`
- Test: `internal/hub/download_test.go`

- [x] **Step 1: Write the failing tests**

```go
package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveFile serves content at /org/repo/resolve/main/<name> with Range support.
func hubFileServer(t *testing.T, content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasPrefix(r.URL.Path, "/org/repo/resolve/main/"))
		data := []byte(content)
		if rng := r.Header.Get("Range"); rng != "" {
			var off int64
			fmt.Sscanf(rng, "bytes=%d-", &off)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)-int(off)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[off:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	}))
}

func waitForState(t *testing.T, m *Manager, id, state string) shared_DownloadInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range m.Snapshot() {
			if d.ID == id && d.State == state {
				return d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("download %s never reached state %s, have: %+v", id, state, m.Snapshot())
	panic("unreachable")
}

func TestHubManager_Download(t *testing.T) {
	ts := hubFileServer(t, "hello gguf bytes")
	defer ts.Close()
	dir := t.TempDir()
	cfgPath := writeTestConfig(t)

	m := NewManager(Options{BaseURL: ts.URL, ConfigPath: cfgPath})
	reloaded := make(chan struct{}, 1)
	m.SetReloadFunc(func() { reloaded <- struct{}{} })

	id, err := m.StartDownload(DownloadOpts{
		ModelsDir: dir,
		Repo:      "org/repo",
		Files:     []RepoFile{{Name: "model-Q4.gguf", Size: 16}},
		ModelID:   "model-q4",
		Name:      "model-Q4",
		Cmd:       "llama-server --port ${PORT} -m " + filepath.Join(dir, "model-Q4.gguf"),
	})
	require.NoError(t, err)
	assert.Equal(t, "model-q4", id)

	done := waitForState(t, m, id, "completed")
	assert.Equal(t, int64(16), done.DownloadedBytes)

	data, err := os.ReadFile(filepath.Join(dir, "model-Q4.gguf"))
	require.NoError(t, err)
	assert.Equal(t, "hello gguf bytes", string(data))

	// config entry was appended and reload requested
	cfgData, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(cfgData), `"model-q4":`)
	select {
	case <-reloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("reload was not requested")
	}
}

func TestHubManager_DownloadResume(t *testing.T) {
	ts := hubFileServer(t, "0123456789")
	defer ts.Close()
	dir := t.TempDir()

	// pre-existing partial file: only the tail should be fetched
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model-Q4.gguf.part"), []byte("01234"), 0644))

	m := NewManager(Options{BaseURL: ts.URL, ConfigPath: writeTestConfig(t)})
	id, err := m.StartDownload(DownloadOpts{
		ModelsDir: dir,
		Repo:      "org/repo",
		Files:     []RepoFile{{Name: "model-Q4.gguf", Size: 10}},
		ModelID:   "model-q4",
		Cmd:       "llama-server --port ${PORT} -m x",
	})
	require.NoError(t, err)
	waitForState(t, m, id, "completed")

	data, err := os.ReadFile(filepath.Join(dir, "model-Q4.gguf"))
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(data))
}

func TestHubManager_DownloadDuplicateRejected(t *testing.T) {
	// slow server so the first job is still active when the second starts
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL, ConfigPath: writeTestConfig(t)})
	opts := DownloadOpts{
		ModelsDir: t.TempDir(), Repo: "org/repo",
		Files: []RepoFile{{Name: "f.gguf", Size: 1}}, ModelID: "f", Cmd: "x",
	}
	_, err := m.StartDownload(opts)
	require.NoError(t, err)
	_, err = m.StartDownload(opts)
	require.Error(t, err)
	m.Cancel("f")
}

func TestHubManager_Cancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		for i := 0; i < 1000000; i++ {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			if i%1000 == 0 {
				time.Sleep(time.Millisecond)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL, ConfigPath: writeTestConfig(t)})
	id, err := m.StartDownload(DownloadOpts{
		ModelsDir: t.TempDir(), Repo: "org/repo",
		Files: []RepoFile{{Name: "big.gguf", Size: 1000000}}, ModelID: "big", Cmd: "x",
	})
	require.NoError(t, err)
	require.True(t, m.Cancel(id))
	waitForState(t, m, id, "cancelled")
}
```

Note: replace `shared_DownloadInfo` in the helper with the real type `shared.DownloadInfo` and import `github.com/mostlygeek/llama-swap/internal/shared`.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test -v -run 'TestHubManager_(Download|Cancel)' ./internal/hub/`
Expected: FAIL (StartDownload, DownloadOpts, Snapshot, Cancel undefined)

- [x] **Step 3: Implement `internal/hub/download.go`**

```go
package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/shared"
)

const progressInterval = 500 * time.Millisecond

// DownloadOpts describes one download job. Files must be the full part set
// (one entry for single-file models). Cmd is the already-rendered cmd for the
// auto-added config entry (with ${MODEL_PATH} substituted by the caller).
type DownloadOpts struct {
	ModelsDir string
	Token     string
	Repo      string
	Files     []RepoFile
	ModelID   string
	Name      string
	Cmd       string
}

// StartDownload begins a download job in the background and returns its ID
// (the model ID). It fails fast on duplicate active jobs or insufficient
// disk space.
func (m *Manager) StartDownload(opts DownloadOpts) (string, error) {
	if len(opts.Files) == 0 {
		return "", fmt.Errorf("no files to download")
	}
	var total int64
	for _, f := range opts.Files {
		total += f.Size
	}
	if usage, err := disk.Usage(opts.ModelsDir); err == nil && usage.Free < uint64(total) {
		return "", fmt.Errorf("not enough disk space: need %d bytes, %d free", total, usage.Free)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if d, exists := m.downloads[opts.ModelID]; exists && d.info.State == "downloading" {
		return "", fmt.Errorf("download already in progress for %s", opts.ModelID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := &download{
		info: shared.DownloadInfo{
			ID:         opts.ModelID,
			Repo:       opts.Repo,
			File:       opts.Files[0].Name,
			ModelID:    opts.ModelID,
			State:      "downloading",
			TotalBytes: total,
		},
		cancel: cancel,
	}
	m.downloads[opts.ModelID] = d
	go m.runDownload(ctx, d, opts)
	m.emitLocked()
	return opts.ModelID, nil
}

// Cancel stops an active download. The .part file is kept for resume.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok || d.info.State != "downloading" {
		return false
	}
	d.cancel()
	return true
}

// Snapshot returns the current state of every download job.
func (m *Manager) Snapshot() []shared.DownloadInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]shared.DownloadInfo, 0, len(m.downloads))
	for _, d := range m.downloads {
		out = append(out, d.info)
	}
	return out
}

// emitLocked publishes a full snapshot; callers must hold m.mu.
func (m *Manager) emitLocked() {
	out := make([]shared.DownloadInfo, 0, len(m.downloads))
	for _, d := range m.downloads {
		out = append(out, d.info)
	}
	event.Emit(shared.DownloadStatusEvent{Downloads: out})
}

func (m *Manager) setState(d *download, state, errMsg string) {
	m.mu.Lock()
	d.info.State = state
	d.info.Error = errMsg
	d.info.SpeedBps = 0
	m.emitLocked()
	m.mu.Unlock()
}

func (m *Manager) runDownload(ctx context.Context, d *download, opts DownloadOpts) {
	var doneBytes int64
	for _, f := range opts.Files {
		n, err := m.downloadFile(ctx, d, opts, f, doneBytes)
		if err != nil {
			if ctx.Err() != nil {
				m.setState(d, "cancelled", "")
			} else {
				if m.logger != nil {
					m.logger.Warnf("hub: download %s failed: %v", d.info.ID, err)
				}
				m.setState(d, "error", err.Error())
			}
			return
		}
		doneBytes += n
	}

	if err := AddModelEntry(m.configPath, opts.ModelID, opts.Name, opts.Cmd); err != nil {
		m.setState(d, "error", fmt.Sprintf("downloaded, but failed to update config: %v", err))
		return
	}
	m.setState(d, "completed", "")
	if m.logger != nil {
		m.logger.Infof("hub: download %s completed, added model entry", d.info.ID)
	}
	if m.reloadFn != nil {
		go m.reloadFn()
	}
}

// downloadFile fetches one file to modelsDir with .part resume. Returns the
// file's full size on success. prevBytes is the byte count of already-completed
// files in this job, used for progress reporting.
func (m *Manager) downloadFile(ctx context.Context, d *download, opts DownloadOpts, f RepoFile, prevBytes int64) (int64, error) {
	dest := filepath.Join(opts.ModelsDir, f.Name)
	if st, err := os.Stat(dest); err == nil {
		return st.Size(), nil // already downloaded
	}

	part := dest + ".part"
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	u := fmt.Sprintf("%s/%s/resolve/main/%s", m.baseURL, opts.Repo, url.PathEscape(f.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		offset = 0 // server ignored Range; start over
		flags |= os.O_TRUNC
	default:
		return 0, fmt.Errorf("huggingface returned %s for %s", resp.Status, f.Name)
	}

	out, err := os.OpenFile(part, flags, 0644)
	if err != nil {
		return 0, err
	}

	written := offset
	lastEmit := time.Now()
	lastBytes := written
	buf := make([]byte, 256*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return 0, werr
			}
			written += int64(n)
			if now := time.Now(); now.Sub(lastEmit) >= progressInterval {
				secs := now.Sub(lastEmit).Seconds()
				m.mu.Lock()
				d.info.DownloadedBytes = prevBytes + written
				d.info.SpeedBps = float64(written-lastBytes) / secs
				m.emitLocked()
				m.mu.Unlock()
				lastEmit = now
				lastBytes = written
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return 0, readErr
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(part, dest); err != nil {
		return 0, err
	}

	m.mu.Lock()
	d.info.DownloadedBytes = prevBytes + written
	m.emitLocked()
	m.mu.Unlock()
	return written, nil
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -v -run 'TestHubManager_' ./internal/hub/`
Expected: PASS (all hub tests)

- [x] **Step 5: Commit**

```bash
gofmt -w internal/hub/
git add internal/hub/
git commit -m "hub: add download manager with resume, cancel and progress events"
```

---

### Task 6: Server API endpoints + SSE wiring

**Files:**
- Create: `internal/server/hubapi.go`
- Modify: `internal/server/server.go` (Server struct ~line 23, routes() ~line 234)
- Modify: `internal/server/apigroup.go` (messageType consts ~line 177, handleAPIEvents ~line 241)
- Test: `internal/server/hubapi_test.go`

- [x] **Step 1: Write the failing tests**

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/hub"
)

func newHubTestServer(t *testing.T, yamlCfg string) *Server {
	t.Helper()
	cfg, err := config.LoadConfigFromReader(strings.NewReader(yamlCfg))
	require.NoError(t, err)
	muxLog, proxyLog, upstreamLog := NewLoggers("none")
	srv, err := New(cfg, muxLog, proxyLog, upstreamLog, nil, BuildInfo{})
	require.NoError(t, err)
	t.Cleanup(func() { srv.Shutdown(time.Second) })
	return srv
}

func TestHubAPI_DisabledWithoutModelsDir(t *testing.T) {
	srv := newHubTestServer(t, "models: {}\n")
	srv.SetHubManager(hub.NewManager(hub.Options{}))

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/popular", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "modelsDir")
}

func TestHubAPI_DisabledWithoutManager(t *testing.T) {
	srv := newHubTestServer(t, "modelsDir: /tmp\nmodels: {}\n")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/downloads", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHubAPI_DownloadsSnapshot(t *testing.T) {
	srv := newHubTestServer(t, "modelsDir: /tmp\nmodels: {}\n")
	srv.SetHubManager(hub.NewManager(hub.Options{}))

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/downloads", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}
```

If `internal/server` already has an equivalent test-server helper, reuse it instead of `newHubTestServer`.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test -v -run 'TestHubAPI_' ./internal/server/`
Expected: FAIL (SetHubManager undefined)

- [x] **Step 3: Implement**

**`internal/server/server.go`** — add field to Server struct:

```go
	hub *hub.Manager
```

with import `"github.com/mostlygeek/llama-swap/internal/hub"`. Add setter after `New`:

```go
// SetHubManager attaches the HuggingFace download manager. The manager is
// created once in main and outlives config reloads; call this on every
// server built, before it starts serving.
func (s *Server) SetHubManager(m *hub.Manager) {
	s.hub = m
}
```

In `routes()`, after the `/api/captures/{id}` line:

```go
	// HuggingFace hub (model downloads).
	mux.Handle("GET /api/hub/popular", apiChain.ThenFunc(s.handleHubPopular))
	mux.Handle("GET /api/hub/search", apiChain.ThenFunc(s.handleHubSearch))
	mux.Handle("GET /api/hub/repo/{repo...}", apiChain.ThenFunc(s.handleHubRepo))
	mux.Handle("GET /api/hub/downloads", apiChain.ThenFunc(s.handleHubDownloads))
	mux.Handle("POST /api/hub/download", apiChain.ThenFunc(s.handleHubDownload))
	mux.Handle("POST /api/hub/download/cancel", apiChain.ThenFunc(s.handleHubDownloadCancel))
	mux.Handle("POST /api/hub/delete", apiChain.ThenFunc(s.handleHubDelete))
```

**`internal/server/hubapi.go`** (new):

```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/hub"
	"github.com/mostlygeek/llama-swap/internal/router"
)

const hubSearchLimit = 30

// hubEnabled gates every /api/hub endpoint: both a manager and a configured
// modelsDir are required.
func (s *Server) hubEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.hub == nil || s.cfg.ModelsDir == "" {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"hub downloads are disabled: set modelsDir in the configuration file")
		return false
	}
	return true
}

func sendJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHubPopular(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	repos, err := s.hub.Search(r.Context(), s.cfg.HubToken, "", hubSearchLimit)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, repos)
}

func (s *Server) handleHubSearch(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "missing query parameter q")
		return
	}
	repos, err := s.hub.Search(r.Context(), s.cfg.HubToken, q, hubSearchLimit)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, repos)
}

func (s *Server) handleHubRepo(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	repo := r.PathValue("repo")
	files, err := s.hub.ListFiles(r.Context(), s.cfg.HubToken, repo, s.cfg.ModelsDir)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if files == nil {
		files = []hub.RepoFile{}
	}
	sendJSON(w, files)
}

func (s *Server) handleHubDownloads(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "hub downloads are disabled")
		return
	}
	sendJSON(w, s.hub.Snapshot())
}

func (s *Server) handleHubDownload(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	var req struct {
		Repo string `json:"repo"`
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" || req.File == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with repo and file")
		return
	}

	files, err := s.hub.ListFiles(r.Context(), s.cfg.HubToken, req.Repo, s.cfg.ModelsDir)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	parts := hub.PartSet(files, req.File)
	if len(parts) == 0 {
		router.SendResponse(w, r, http.StatusNotFound, "file not found in repo")
		return
	}

	modelID := hub.DeriveModelID(req.File)
	if _, exists := s.cfg.Models[modelID]; exists {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", modelID, i)
			if _, exists := s.cfg.Models[candidate]; !exists {
				modelID = candidate
				break
			}
		}
	}

	mainPath := filepath.Join(s.cfg.ModelsDir, parts[0].Name)
	cmd := strings.ReplaceAll(s.cfg.HubCmdTemplate, "${MODEL_PATH}", mainPath)

	id, err := s.hub.StartDownload(hub.DownloadOpts{
		ModelsDir: s.cfg.ModelsDir,
		Token:     s.cfg.HubToken,
		Repo:      req.Repo,
		Files:     parts,
		ModelID:   modelID,
		Name:      strings.TrimSuffix(req.File, ".gguf"),
		Cmd:       cmd,
	})
	if err != nil {
		router.SendResponse(w, r, http.StatusConflict, err.Error())
		return
	}
	sendJSON(w, map[string]string{"id": id})
}

func (s *Server) handleHubDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "hub downloads are disabled")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with id")
		return
	}
	if !s.hub.Cancel(req.ID) {
		router.SendResponse(w, r, http.StatusNotFound, "no active download with that id")
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}

// handleHubDelete unloads a model, deletes its GGUF files (only those inside
// modelsDir), removes its config entry, and triggers a config reload.
func (s *Server) handleHubDelete(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ModelID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with modelId")
		return
	}

	realName, found := s.cfg.RealModelName(req.ModelID)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]

	if state, running := s.local.RunningModels()[realName]; running && string(state) == "starting" {
		router.SendResponse(w, r, http.StatusConflict, "model is starting, try again when it is ready")
		return
	}
	s.local.Unload(apiUnloadTimeout, realName)

	deleted, skipped := deleteModelFiles(mc.Cmd, s.cfg.ModelsDir)

	if _, err := s.hub.RemoveModel(realName); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("failed to update config: %v", err))
		return
	}

	sendJSON(w, map[string]any{"deleted": deleted, "skipped": skipped})
}

// deleteModelFiles parses a macro-expanded cmd, collects -m/--model/--mmproj
// file arguments, and removes those inside modelsDir (plus stale .part
// files). Paths outside modelsDir are reported as skipped.
func deleteModelFiles(cmd, modelsDir string) (deleted, skipped []string) {
	deleted, skipped = []string{}, []string{}
	args, err := config.SanitizeCommand(cmd)
	if err != nil {
		return deleted, skipped
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-m", "--model", "--mmproj":
			path := args[i+1]
			abs, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(modelsDir, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				skipped = append(skipped, path)
				continue
			}
			if err := os.Remove(abs); err == nil {
				deleted = append(deleted, path)
			}
			os.Remove(abs + ".part")
		}
	}
	return deleted, skipped
}
```

**`internal/hub/manager.go`** — add the RemoveModel wrapper used above:

```go
// RemoveModel deletes the model's config.yaml entry and requests a reload.
func (m *Manager) RemoveModel(modelID string) (bool, error) {
	found, err := RemoveModelEntry(m.configPath, modelID)
	if err != nil {
		return found, err
	}
	if found && m.reloadFn != nil {
		go m.reloadFn()
	}
	return found, nil
}
```

**`internal/server/apigroup.go`** — add to the messageType consts:

```go
	msgTypeDownloads messageType = "downloadStatus"
```

In `handleAPIEvents`, next to the other `sendX` closures:

```go
	sendDownloads := func(downloads []shared.DownloadInfo) {
		if j, err := json.Marshal(downloads); err == nil {
			send(messageEnvelope{Type: msgTypeDownloads, Data: string(j)})
		}
	}
```

Next to the other `defer event.On(...)` lines:

```go
	defer event.On(func(e shared.DownloadStatusEvent) { sendDownloads(e.Downloads) })()
```

In the initial payload block:

```go
	if s.hub != nil {
		sendDownloads(s.hub.Snapshot())
	}
```

Note: `RunningModels()` returns a map whose value type is a process-state
string type — check its actual signature in `internal/router` and convert with
`string(...)` as shown, adjusting if the value is already `string`.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -v -run 'TestHubAPI_' ./internal/server/`
Expected: PASS (3 tests)

- [x] **Step 5: Run the wider suite**

Run: `make test-dev`
Expected: all tests pass; fix any staticcheck findings in new code.

- [x] **Step 6: Commit**

```bash
gofmt -w internal/server/ internal/hub/
git add internal/server/ internal/hub/
git commit -m "server: add /api/hub endpoints and downloadStatus SSE events"
```

---

### Task 7: Wire the manager in main (`llama-swap.go`)

**Files:**
- Modify: `llama-swap.go` (after config load ~line 95, after `reload` definition ~line 226, inside `reload` after `server.New`)

- [x] **Step 1: Implement**

After `buildInfo := server.BuildInfo{...}` (~line 146):

```go
	// hubManager outlives config reloads so active downloads keep running.
	hubManager := hub.NewManager(hub.Options{
		ConfigPath: configPath,
		Logger:     proxyLog,
	})
```

Add import `"github.com/mostlygeek/llama-swap/internal/hub"`.

After `initialSrv, err := server.New(...)` succeeds:

```go
	initialSrv.SetHubManager(hubManager)
```

Inside `reload()`, after `newSrv, err := server.New(...)` succeeds (before the `activeMu.Lock()` swap):

```go
		newSrv.SetHubManager(hubManager)
```

After the `reload := func() {...}` definition:

```go
	hubManager.SetReloadFunc(reload)
```

- [x] **Step 2: Verify build and full test run**

Run: `go build -o build/llama-swap . && make test-dev`
Expected: builds, tests pass

- [x] **Step 3: Commit**

```bash
gofmt -w llama-swap.go
git add llama-swap.go
git commit -m "main: create hub manager and wire it through config reloads"
```

---

### Task 8: UI types and API store additions

**Files:**
- Modify: `ui-svelte/src/lib/types.ts`
- Modify: `ui-svelte/src/stores/api.ts`

- [x] **Step 1: Add types to `ui-svelte/src/lib/types.ts`**

Extend the envelope union (line ~94):

```ts
export interface APIEventEnvelope {
  type: "modelStatus" | "logData" | "metrics" | "inflight" | "perfsys" | "perfgpu" | "downloadStatus";
  data: string;
}
```

Append:

```ts
export type DownloadState = "downloading" | "completed" | "error" | "cancelled";

export interface DownloadInfo {
  id: string;
  repo: string;
  file: string;
  modelId: string;
  state: DownloadState;
  totalBytes: number;
  downloadedBytes: number;
  speedBps: number;
  error?: string;
}

export interface HubRepo {
  id: string;
  downloads: number;
  likes: number;
}

export interface HubFile {
  name: string;
  size: number;
  quant: string;
  downloaded: boolean;
}
```

- [x] **Step 2: Add store + fetch helpers to `ui-svelte/src/stores/api.ts`**

Import the new types, add a store next to `models`:

```ts
export const downloads = writable<DownloadInfo[]>([]);
```

In the SSE `onmessage` switch, add a case:

```ts
          case "downloadStatus": {
            const newDownloads = JSON.parse(message.data) as DownloadInfo[] | null;
            downloads.set(newDownloads ?? []);
            break;
          }
```

Append fetch helpers at the end of the file:

```ts
// hubDisabledError is thrown when the server reports the hub feature is off
// (modelsDir not configured).
export class HubDisabledError extends Error {}

async function hubFetch<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (response.status === 503) {
    throw new HubDisabledError(await response.text());
  }
  if (!response.ok) {
    throw new Error(await response.text());
  }
  return (await response.json()) as T;
}

export async function hubPopular(): Promise<HubRepo[]> {
  return hubFetch<HubRepo[]>("/api/hub/popular");
}

export async function hubSearch(query: string): Promise<HubRepo[]> {
  return hubFetch<HubRepo[]>(`/api/hub/search?q=${encodeURIComponent(query)}`);
}

export async function hubRepoFiles(repo: string): Promise<HubFile[]> {
  return hubFetch<HubFile[]>(`/api/hub/repo/${repo}`);
}

export async function hubDownload(repo: string, file: string): Promise<void> {
  await hubFetch(`/api/hub/download`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ repo, file }),
  });
}

export async function hubCancelDownload(id: string): Promise<void> {
  await hubFetch(`/api/hub/download/cancel`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id }),
  });
}

export async function hubDeleteModel(modelId: string): Promise<void> {
  await hubFetch(`/api/hub/delete`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ modelId }),
  });
}
```

- [x] **Step 3: Verify**

Run: `cd ui-svelte && npm run check`
Expected: no new errors

- [x] **Step 4: Commit**

```bash
git add ui-svelte/src/lib/types.ts ui-svelte/src/stores/api.ts
git commit -m "ui: add hub download types, store and API helpers"
```

---

### Task 9: Discover tab (search/popular/download UI)

**Files:**
- Create: `ui-svelte/src/components/DiscoverPanel.svelte`
- Modify: `ui-svelte/src/routes/Models.svelte` (add Installed/Discover tabs)

- [x] **Step 1: Create `ui-svelte/src/components/DiscoverPanel.svelte`**

```svelte
<script lang="ts">
  import { hubPopular, hubSearch, hubRepoFiles, hubDownload, downloads, HubDisabledError } from "../stores/api";
  import type { HubRepo, HubFile } from "../lib/types";

  let query = $state("");
  let repos = $state<HubRepo[]>([]);
  let loading = $state(false);
  let disabledMessage = $state("");
  let errorMessage = $state("");

  let expandedRepo = $state<string | null>(null);
  let repoFiles = $state<HubFile[]>([]);
  let filesLoading = $state(false);

  // files already being downloaded (any state) keyed by repo+file
  let activeFiles = $derived(new Set($downloads.filter((d) => d.state === "downloading").map((d) => `${d.repo}/${d.file}`)));
  let completedFiles = $derived(new Set($downloads.filter((d) => d.state === "completed").map((d) => `${d.repo}/${d.file}`)));

  async function loadRepos(): Promise<void> {
    loading = true;
    errorMessage = "";
    try {
      repos = query.trim() ? await hubSearch(query.trim()) : await hubPopular();
    } catch (e) {
      if (e instanceof HubDisabledError) {
        disabledMessage = e.message;
      } else {
        errorMessage = e instanceof Error ? e.message : String(e);
      }
    } finally {
      loading = false;
    }
  }

  async function toggleRepo(repoId: string): Promise<void> {
    if (expandedRepo === repoId) {
      expandedRepo = null;
      return;
    }
    expandedRepo = repoId;
    repoFiles = [];
    filesLoading = true;
    errorMessage = "";
    try {
      repoFiles = await hubRepoFiles(repoId);
    } catch (e) {
      errorMessage = e instanceof Error ? e.message : String(e);
    } finally {
      filesLoading = false;
    }
  }

  async function startDownload(repoId: string, file: HubFile): Promise<void> {
    errorMessage = "";
    try {
      await hubDownload(repoId, file.name);
    } catch (e) {
      errorMessage = e instanceof Error ? e.message : String(e);
    }
  }

  function formatBytes(n: number): string {
    if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`;
    if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`;
    return `${(n / 1024).toFixed(0)} KB`;
  }

  $effect(() => {
    loadRepos();
  });
</script>

<div class="card h-full flex flex-col">
  <div class="shrink-0">
    <h2>Discover</h2>
    {#if disabledMessage}
      <p class="text-txtsecondary mt-2">
        Hub downloads are disabled. Set <code>modelsDir</code> in your configuration file to enable downloading
        models from HuggingFace.
      </p>
    {:else}
      <form
        class="flex gap-2 mt-2"
        onsubmit={(e) => {
          e.preventDefault();
          loadRepos();
        }}
      >
        <input
          type="text"
          class="flex-1 px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
          placeholder="Search GGUF models on HuggingFace…"
          bind:value={query}
        />
        <button class="btn text-base" type="submit" disabled={loading}>{loading ? "Searching…" : "Search"}</button>
      </form>
    {/if}
    {#if errorMessage}
      <p class="text-red-500 mt-2">{errorMessage}</p>
    {/if}
  </div>

  {#if !disabledMessage}
    <div class="flex-1 overflow-y-auto mt-2">
      <table class="w-full">
        <thead class="sticky top-0 bg-card z-10">
          <tr class="text-left border-b border-gray-200 dark:border-white/10 bg-surface">
            <th>Repository</th>
            <th class="w-24 text-right">Downloads</th>
            <th class="w-16 text-right">Likes</th>
          </tr>
        </thead>
        <tbody>
          {#each repos as repo (repo.id)}
            <tr class="border-b hover:bg-secondary-hover border-gray-200 cursor-pointer" onclick={() => toggleRepo(repo.id)}>
              <td class="font-semibold">{repo.id}</td>
              <td class="text-right">{repo.downloads.toLocaleString()}</td>
              <td class="text-right">{repo.likes.toLocaleString()}</td>
            </tr>
            {#if expandedRepo === repo.id}
              <tr class="border-b border-gray-200">
                <td colspan="3" class="pl-8 py-2">
                  {#if filesLoading}
                    <p class="text-txtsecondary">Loading files…</p>
                  {:else if repoFiles.length === 0}
                    <p class="text-txtsecondary">No GGUF files found in this repo.</p>
                  {:else}
                    <table class="w-full">
                      <tbody>
                        {#each repoFiles as file (file.name)}
                          <tr>
                            <td>{file.name}</td>
                            <td class="w-20">{file.quant}</td>
                            <td class="w-24 text-right">{formatBytes(file.size)}</td>
                            <td class="w-32 text-right">
                              {#if file.downloaded || completedFiles.has(`${repo.id}/${file.name}`)}
                                <span class="status status--ready">Downloaded ✓</span>
                              {:else if activeFiles.has(`${repo.id}/${file.name}`)}
                                <span class="status status--starting">downloading</span>
                              {:else}
                                <button class="btn btn--sm" onclick={() => startDownload(repo.id, file)}>Download</button>
                              {/if}
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  {/if}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
```

- [x] **Step 2: Add tabs to `ui-svelte/src/routes/Models.svelte`**

Replace the file's contents with:

```svelte
<script lang="ts">
  import { isNarrow } from "../stores/theme";
  import { upstreamLogs } from "../stores/api";
  import ModelsPanel from "../components/ModelsPanel.svelte";
  import DiscoverPanel from "../components/DiscoverPanel.svelte";
  import LogPanel from "../components/LogPanel.svelte";
  import ResizablePanels from "../components/ResizablePanels.svelte";

  let direction = $derived<"horizontal" | "vertical">($isNarrow ? "vertical" : "horizontal");
  let tab = $state<"installed" | "discover">("installed");
</script>

<ResizablePanels {direction} storageKey="models-panel-group">
  {#snippet leftPanel()}
    <div class="h-full flex flex-col">
      <div class="shrink-0 flex gap-2 mb-2">
        <button
          class="btn text-base"
          class:font-bold={tab === "installed"}
          class:underline={tab === "installed"}
          onclick={() => (tab = "installed")}
        >
          Installed
        </button>
        <button
          class="btn text-base"
          class:font-bold={tab === "discover"}
          class:underline={tab === "discover"}
          onclick={() => (tab = "discover")}
        >
          Discover
        </button>
      </div>
      <div class="flex-1 min-h-0">
        {#if tab === "installed"}
          <ModelsPanel />
        {:else}
          <DiscoverPanel />
        {/if}
      </div>
    </div>
  {/snippet}
  {#snippet rightPanel()}
    <LogPanel id="modelsupstream" title="Upstream Logs" logData={$upstreamLogs} />
  {/snippet}
</ResizablePanels>
```

- [x] **Step 3: Verify**

Run: `cd ui-svelte && npm run check`
Expected: no new errors

- [x] **Step 4: Commit**

```bash
git add ui-svelte/src/components/DiscoverPanel.svelte ui-svelte/src/routes/Models.svelte
git commit -m "ui: add Discover tab for browsing and downloading HF models"
```

---

### Task 10: Installed tab — download progress rows and delete

**Files:**
- Modify: `ui-svelte/src/components/ModelsPanel.svelte`

- [x] **Step 1: Implement**

In the `<script>` block, extend the imports and add state/handlers:

```ts
  import { models, loadModel, unloadAllModels, unloadSingleModel, downloads, hubCancelDownload, hubDeleteModel } from "../stores/api";
```

```ts
  let deleteError = $state("");

  let activeDownloads = $derived($downloads.filter((d) => d.state !== "completed"));

  async function handleDelete(model: Model): Promise<void> {
    if (!confirm(`Delete "${model.id}"?\n\nThis removes its GGUF file(s) from the models directory and its entry from the configuration file.`)) {
      return;
    }
    deleteError = "";
    try {
      await hubDeleteModel(model.id);
    } catch (e) {
      deleteError = e instanceof Error ? e.message : String(e);
    }
  }

  function formatBytes(n: number): string {
    if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`;
    if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`;
    return `${(n / 1024).toFixed(0)} KB`;
  }

  function progressPct(d: { downloadedBytes: number; totalBytes: number }): number {
    return d.totalBytes > 0 ? Math.min(100, (d.downloadedBytes / d.totalBytes) * 100) : 0;
  }
```

In the template, directly above the `<table class="w-full">` for models (inside the scrollable div), add the downloads section:

```svelte
    {#if deleteError}
      <p class="text-red-500">{deleteError}</p>
    {/if}
    {#if activeDownloads.length > 0}
      <h3 class="mb-2">Downloads</h3>
      {#each activeDownloads as d (d.id)}
        <div class="mb-2 p-2 border border-gray-200 dark:border-white/10 rounded">
          <div class="flex justify-between items-center">
            <span class="font-semibold">{d.file}</span>
            {#if d.state === "downloading"}
              <button class="btn btn--sm" onclick={() => hubCancelDownload(d.id)}>Cancel</button>
            {/if}
          </div>
          {#if d.state === "downloading"}
            <div class="w-full bg-surface rounded h-2 mt-1">
              <div class="bg-primary h-2 rounded" style="width: {progressPct(d)}%"></div>
            </div>
            <p class="text-xs text-txtsecondary mt-1">
              {formatBytes(d.downloadedBytes)} / {formatBytes(d.totalBytes)} — {formatBytes(d.speedBps)}/s
            </p>
          {:else if d.state === "error"}
            <p class="text-xs text-red-500 mt-1">{d.error}</p>
          {:else}
            <p class="text-xs text-txtsecondary mt-1">{d.state}</p>
          {/if}
        </div>
      {/each}
    {/if}
```

Note: if the `bg-primary` utility class does not exist in this Tailwind setup, use the same accent class other components use for emphasis (check `ActivityStats.svelte`/`PerformanceChart.svelte` for a precedent) or an inline style.

In the models table, add a delete button to the actions cell (the `<td class="w-12">`), turning it into:

```svelte
            <td class="w-24">
              <div class="flex gap-1">
                {#if model.state === "stopped"}
                  <button class="btn btn--sm" onclick={() => loadModel(model.id)}>Load</button>
                {:else}
                  <button class="btn btn--sm" onclick={() => unloadSingleModel(model.id)} disabled={model.state !== "ready"}>Unload</button>
                {/if}
                <button class="btn btn--sm" title="Delete model" aria-label="Delete model" onclick={() => handleDelete(model)} disabled={model.state === "starting"}>
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-4 h-4">
                    <path fill-rule="evenodd" d="M16.5 4.478v.227a48.816 48.816 0 0 1 3.878.512.75.75 0 1 1-.256 1.478l-.209-.035-1.005 13.07a3 3 0 0 1-2.991 2.77H8.084a3 3 0 0 1-2.991-2.77L4.087 6.66l-.209.035a.75.75 0 0 1-.256-1.478A48.567 48.567 0 0 1 7.5 4.705v-.227c0-1.564 1.213-2.9 2.816-2.951a52.662 52.662 0 0 1 3.369 0c1.603.051 2.815 1.387 2.815 2.951Zm-6.136-1.452a51.196 51.196 0 0 1 3.273 0C14.39 3.05 15 3.684 15 4.478v.113a49.488 49.488 0 0 0-6 0v-.113c0-.794.609-1.428 1.364-1.452Zm-.355 5.945a.75.75 0 1 0-1.5.058l.347 9a.75.75 0 1 0 1.499-.058l-.346-9Zm5.48.058a.75.75 0 1 0-1.498-.058l-.347 9a.75.75 0 0 0 1.5.058l.345-9Z" clip-rule="evenodd" />
                  </svg>
                </button>
              </div>
            </td>
```

- [x] **Step 2: Verify**

Run: `cd ui-svelte && npm run check`
Expected: no new errors

- [x] **Step 3: Commit**

```bash
git add ui-svelte/src/components/ModelsPanel.svelte
git commit -m "ui: show download progress and add model delete to Models page"
```

---

### Task 11: Final verification

- [x] **Step 1: Full Go test suite**

Run: `make test-all`
Expected: PASS (includes -race; watch for races in hub manager — all `d.info` access must hold `m.mu`)

- [x] **Step 2: UI suite**

Run: `make test-ui`
Expected: svelte-check and vitest pass

- [x] **Step 3: Build**

Run: `go build -o build/llama-swap . && cd ui-svelte && npm run build`
Expected: success

- [x] **Step 4: Commit any remaining fixes**

```bash
gofmt -w internal/ llama-swap.go
git add -A
git commit -m "proxy: finish HuggingFace model download feature" || true
```
