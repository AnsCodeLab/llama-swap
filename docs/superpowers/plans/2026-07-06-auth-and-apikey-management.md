# Username/Password Protection + API Key Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Protect every route llama-swap serves with an optional HTTP Basic Auth username/password (alternate credential to the existing `apiKeys`), and add a Settings page in the UI to configure that username/password and to generate/label/revoke API keys at runtime instead of hand-editing YAML.

**Architecture:** A single global auth middleware wraps the entire HTTP handler (UI, health, metrics, inference, everything) and admits a request if it presents either a valid API key or valid Basic Auth username/password. `apiKeys` gains optional `label`/`createdAt`/`id` metadata (back-compat with bare strings). Runtime writes (new API keys, credential changes) go through a config-file editor extracted from the existing hub-download feature's mutex-protected read-modify-write-encode-atomic-write cycle, so both features share the one lock instead of racing on `config.yaml`.

**Tech Stack:** Go (`net/http`, `gopkg.in/yaml.v3`, `crypto/rand`, `crypto/subtle`), Svelte 5 + TypeScript (ui-svelte), testify (Go tests), vitest (UI tests, where the project already has coverage).

## Global Constraints

- Follow test naming: `TestConfig_<name>`, `TestServer_<name>` (per `AGENTS.md`).
- Run `gofmt -w <file>` on every changed Go file before committing.
- Build Go binaries into `./build/` (not relevant to this plan — no new binaries).
- Use `go test -v -run <pattern>` to run new tests individually; use `make test-dev` after any change under `internal/` (runs `go test` + `staticcheck`); use `make test-ui` after UI changes; use `make test-all` before the final commit.
- API keys and username/password are stored in plaintext in `config.yaml`, consistent with existing `apiKeys` behavior, and support `${env.VAR}` macro substitution for free (substitution happens at the raw-YAML-string level before parsing, in `LoadConfigFromReader`).
- A request is admitted if it presents *either* a valid API key *or* valid Basic Auth username/password (alternate credentials, not both required).
- Never let a plaintext API key or password appear in a URL path or an access log line (secrets go in JSON bodies only; deletions reference a non-secret `id`).

---

### Task 1: Extract shared config-file-editing infrastructure into `internal/config`

`internal/hub/configedit.go` already has a mutex-protected read-modify-write-encode-atomic-write cycle for `config.yaml` (added in commit `9fe5619` after two concurrent writers silently dropped each other's edits). The new Settings feature needs the same safe write path for a different top-level key (`apiKeys`, `auth`) — reusing the *same* mutex and atomic-write code is required, or the exact race that `9fe5619` fixed reappears with two independent lock-protected-but-mutually-unaware writers. This task moves the generic parts out of `hub` into `config` and generalizes "find or create the `models:` mapping" into "find or create any top-level mapping/sequence", with no behavior change.

**Files:**
- Create: `internal/config/edit.go`
- Modify: `internal/hub/configedit.go`
- Test: `internal/config/edit_test.go` (new)
- Test: `internal/hub/configedit_test.go` (existing tests must keep passing unchanged)

**Interfaces:**
- Produces (used by Task 4 and by `hub`): `config.EditConfig(configPath string, fn func(root *yaml.Node) error) error`, `config.MappingChild(root *yaml.Node, key string) (*yaml.Node, error)`, `config.SequenceChild(root *yaml.Node, key string) (*yaml.Node, error)`, `config.StrNode(value string, style yaml.Style) *yaml.Node`.

- [ ] **Step 1: Write the failing test for the generalized helpers**

Create `internal/config/edit_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const editTestConfig = `# my llama-swap config
healthCheckTimeout: 600

models:
  # hand-written entry, comments must survive edits
  "qwen3-4b":
    name: "Qwen3 4B"
    cmd: |
      /usr/bin/llama-server --port ${PORT} -m /models/qwen3-4b.gguf
`

func writeEditTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfig), 0644))
	return path
}

func TestConfigEdit_MappingChild_CreatesWhenAbsent(t *testing.T) {
	path := writeEditTestConfig(t)
	err := EditConfig(path, func(root *yaml.Node) error {
		auth, err := MappingChild(root, "auth")
		if err != nil {
			return err
		}
		auth.Content = append(auth.Content, StrNode("username", 0), StrNode("admin", 0))
		return nil
	})
	require.NoError(t, err)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(out), "auth:")
	assert.Contains(t, string(out), "username: admin")
	assert.Contains(t, string(out), "# my llama-swap config")
}

func TestConfigEdit_MappingChild_ReusesExisting(t *testing.T) {
	path := writeEditTestConfig(t)
	err := EditConfig(path, func(root *yaml.Node) error {
		models, err := MappingChild(root, "models")
		if err != nil {
			return err
		}
		models.Content = append(models.Content, StrNode("new-model", 0), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		return nil
	})
	require.NoError(t, err)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, `"qwen3-4b":`)
	assert.Contains(t, s, "new-model:")
}

func TestConfigEdit_SequenceChild_CreatesWhenAbsent(t *testing.T) {
	path := writeEditTestConfig(t)
	err := EditConfig(path, func(root *yaml.Node) error {
		keys, err := SequenceChild(root, "apiKeys")
		if err != nil {
			return err
		}
		keys.Content = append(keys.Content, StrNode("sk-test", yaml.DoubleQuotedStyle))
		return nil
	})
	require.NoError(t, err)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(out), "apiKeys:")
	assert.Contains(t, string(out), `"sk-test"`)
}

func TestConfigEdit_ConcurrentEditsAcrossKeys(t *testing.T) {
	path := writeEditTestConfig(t)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			assert.NoError(t, EditConfig(path, func(root *yaml.Node) error {
				keys, err := SequenceChild(root, "apiKeys")
				if err != nil {
					return err
				}
				keys.Content = append(keys.Content, StrNode("sk-concurrent", yaml.DoubleQuotedStyle))
				return nil
			}))
		}(i)
	}
	wg.Wait()

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, `"qwen3-4b":`, "pre-existing model entry lost to a concurrent-write race")
	count := 0
	for i := 0; i+len("sk-concurrent") <= len(s); i++ {
		if s[i:i+len("sk-concurrent")] == "sk-concurrent" {
			count++
		}
	}
	assert.Equal(t, 5, count, "expected all 5 concurrent apiKeys writes to survive")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/... -run TestConfigEdit -v`
Expected: FAIL — `EditConfig`, `MappingChild`, `SequenceChild`, `StrNode` are undefined.

- [ ] **Step 3: Create `internal/config/edit.go` with the generalized helpers**

```go
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// editMu serializes EditConfig's read-modify-write cycle across the whole
// process. Without it, concurrent callers (e.g. a hub download completing at
// the same time a Settings API key is generated) race on the same config
// file: each reads the pre-edit version, applies its own change in memory,
// then writes back — last writer wins and silently discards every other
// concurrent edit, including unrelated pre-existing entries the racing
// writer's stale read didn't have. See commit 9fe5619 for the original
// regression this guards against.
var editMu sync.Mutex

// EditConfig loads the raw YAML document at configPath, hands the top-level
// mapping node to fn for in-place mutation, and writes the document back
// atomically. Comments and formatting elsewhere in the file are preserved.
func EditConfig(configPath string, fn func(root *yaml.Node) error) error {
	editMu.Lock()
	defer editMu.Unlock()

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

	if err := fn(root); err != nil {
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

// MappingChild finds root's top-level child mapping named key, creating and
// appending an empty one to root if absent. It forces block style so new
// entries render multi-line even when the section previously had `key: {}`
// flow-style content.
func MappingChild(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			node := root.Content[i+1]
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s is not a mapping", key)
			}
			node.Style = 0
			return node, nil
		}
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, StrNode(key, 0), node)
	return node, nil
}

// SequenceChild finds root's top-level child sequence named key, creating
// and appending an empty one to root if absent.
func SequenceChild(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			node := root.Content[i+1]
			if node.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s is not a sequence", key)
			}
			node.Style = 0
			return node, nil
		}
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, StrNode(key, 0), node)
	return node, nil
}

// StrNode builds a scalar string YAML node with the given style (0 for plain).
func StrNode(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
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

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/... -run TestConfigEdit -v`
Expected: PASS (all 4 new tests).

- [ ] **Step 5: Rewrite `internal/hub/configedit.go` as a thin wrapper over `config.EditConfig`**

Replace the entire contents of `internal/hub/configedit.go` with:

```go
// Package hub implements HuggingFace model browsing, downloading, and
// config.yaml management for the web UI.
package hub

import (
	"errors"

	"github.com/mostlygeek/llama-swap/internal/config"
	"gopkg.in/yaml.v3"
)

// AddModelEntry appends a model entry under the top-level models: mapping,
// preserving comments and formatting elsewhere in the file. The cmd is
// written as a literal block scalar.
func AddModelEntry(configPath, modelID, name, cmd string) error {
	return config.EditConfig(configPath, func(root *yaml.Node) error {
		models, err := config.MappingChild(root, "models")
		if err != nil {
			return err
		}

		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if name != "" {
			entry.Content = append(entry.Content,
				config.StrNode("name", 0), config.StrNode(name, 0))
		}
		entry.Content = append(entry.Content,
			config.StrNode("cmd", 0), config.StrNode(cmd+"\n", yaml.LiteralStyle))

		models.Content = append(models.Content,
			config.StrNode(modelID, yaml.DoubleQuotedStyle), entry)
		return nil
	})
}

// RemoveModelEntry deletes a model entry by ID. Returns false if not found.
func RemoveModelEntry(configPath, modelID string) (bool, error) {
	found := false
	err := config.EditConfig(configPath, func(root *yaml.Node) error {
		models, err := config.MappingChild(root, "models")
		if err != nil {
			return err
		}
		for i := 0; i+1 < len(models.Content); i += 2 {
			if models.Content[i].Value == modelID {
				models.Content = append(models.Content[:i], models.Content[i+2:]...)
				found = true
				return nil
			}
		}
		return errNoChange
	})
	if errors.Is(err, errNoChange) {
		return false, nil
	}
	return found, err
}

var errNoChange = errors.New("no change")
```

- [ ] **Step 6: Run the full hub test suite to confirm no regression**

Run: `go test ./internal/hub/... -v`
Expected: PASS — every existing test in `configedit_test.go` (`TestConfigEdit_AddModelEntry`, `TestConfigEdit_AddModelEntryNoModelsSection`, `TestConfigEdit_AddModelEntryFlowStyleModels`, `TestConfigEdit_RemoveModelEntry`, `TestConfigEdit_RemoveModelEntryNotFound`, `TestConfigEdit_ConcurrentAddModelEntry`) and every other hub test continues to pass unchanged.

- [ ] **Step 7: gofmt and commit**

```bash
gofmt -w internal/config/edit.go internal/config/edit_test.go internal/hub/configedit.go
git add internal/config/edit.go internal/config/edit_test.go internal/hub/configedit.go
git commit -m "$(cat <<'EOF'
config,hub: extract shared config-file-editing infra

Move the mutex-protected read-modify-write-encode-atomic-write cycle out
of internal/hub into internal/config and generalize it to any top-level
mapping/sequence, not just models:. The upcoming Settings feature needs
the same safe write path for apiKeys/auth; a second independent mutex
would reintroduce the concurrent-write race fixed in 9fe5619.

- internal/config/edit.go: EditConfig, MappingChild, SequenceChild, StrNode
- internal/hub/configedit.go: AddModelEntry/RemoveModelEntry now thin wrappers
EOF
)"
```

---

### Task 2: Upgrade `apiKeys` config schema to structured entries with back-compat

**Files:**
- Modify: `internal/config/config.go:163` (field declaration), `internal/config/config.go:601-610` (validation loop)
- Modify: `internal/server/auth.go:18-45` (key-comparison loop)
- Modify: `internal/server/auth_test.go:94` (test fixture construction)
- Test: `internal/config/config_test.go` (new back-compat test, plus fixing 4 existing assertions)

**Interfaces:**
- Produces (used by Task 3, 4, 5, 6): `config.APIKeyEntry{ID, Key, Label, CreatedAt string}` with custom `UnmarshalYAML`; `Config.RequiredAPIKeys []APIKeyEntry`.

- [ ] **Step 1: Write the failing test for back-compat parsing**

Add to `internal/config/config_test.go` (append near the existing `TestConfig_APIKeys_EnvMacros` function):

```go
func TestConfig_APIKeys_StructuredAndBareStringEntries(t *testing.T) {
	content := `
apiKeys:
  - key: "sk-labeled"
    label: "CI pipeline"
    createdAt: "2026-07-06T10:00:00Z"
  - "sk-bare-string"
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	cfg, err := LoadConfigFromReader(strings.NewReader(content))
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 2)

	assert.Equal(t, "sk-labeled", cfg.RequiredAPIKeys[0].Key)
	assert.Equal(t, "CI pipeline", cfg.RequiredAPIKeys[0].Label)
	assert.Equal(t, "2026-07-06T10:00:00Z", cfg.RequiredAPIKeys[0].CreatedAt)

	assert.Equal(t, "sk-bare-string", cfg.RequiredAPIKeys[1].Key)
	assert.Empty(t, cfg.RequiredAPIKeys[1].Label)
	assert.Empty(t, cfg.RequiredAPIKeys[1].ID)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/... -run TestConfig_APIKeys_StructuredAndBareStringEntries -v`
Expected: FAIL to compile — `cfg.RequiredAPIKeys[0].Key` doesn't exist yet (`RequiredAPIKeys` is still `[]string`).

- [ ] **Step 3: Add `APIKeyEntry` and change the field type in `config.go`**

In `internal/config/config.go`, add this type near the top of the file, just above the `Config` struct definition (before `type Config struct {` at line 121):

```go
// APIKeyEntry is one entry in apiKeys:. It accepts either a bare YAML string
// (legacy shape, becomes {Key: value}) or a mapping with key/label/createdAt,
// so existing configs keep working unchanged.
type APIKeyEntry struct {
	ID        string `yaml:"id,omitempty"`
	Key       string `yaml:"key"`
	Label     string `yaml:"label,omitempty"`
	CreatedAt string `yaml:"createdAt,omitempty"`
}

// UnmarshalYAML implements the dual bare-string/mapping shape described above.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		e.Key = value.Value
		return nil
	}
	type rawAPIKeyEntry APIKeyEntry
	var raw rawAPIKeyEntry
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*e = APIKeyEntry(raw)
	return nil
}
```

Change line 163 from:

```go
	// support API keys, see issue #433, #50, #251
	RequiredAPIKeys []string `yaml:"apiKeys"`
```

to:

```go
	// support API keys, see issue #433, #50, #251
	RequiredAPIKeys []APIKeyEntry `yaml:"apiKeys"`
```

- [ ] **Step 4: Update the validation loop in `config.go`**

Change (around line 601-610):

```go
	// Validate API keys (env macros already substituted at string level)
	for i, apikey := range config.RequiredAPIKeys {
		if apikey == "" {
			return Config{}, fmt.Errorf("empty api key found in apiKeys")
		}
		if strings.Contains(apikey, " ") {
			return Config{}, fmt.Errorf("api key cannot contain spaces: `%s`", apikey)
		}
		config.RequiredAPIKeys[i] = apikey
	}
```

to:

```go
	// Validate API keys (env macros already substituted at string level)
	for _, entry := range config.RequiredAPIKeys {
		if entry.Key == "" {
			return Config{}, fmt.Errorf("empty api key found in apiKeys")
		}
		if strings.Contains(entry.Key, " ") {
			return Config{}, fmt.Errorf("api key cannot contain spaces: `%s`", entry.Key)
		}
	}
```

- [ ] **Step 5: Run the new test to verify it passes**

Run: `go test ./internal/config/... -run TestConfig_APIKeys_StructuredAndBareStringEntries -v`
Expected: PASS.

- [ ] **Step 6: Fix the 4 existing assertions broken by the type change**

In `internal/config/config_test.go`:

Line 821, change:
```go
		assert.Equal(t, []string{"secret-key-123"}, config.RequiredAPIKeys)
```
to:
```go
		assert.Equal(t, []APIKeyEntry{{Key: "secret-key-123"}}, config.RequiredAPIKeys)
```

Line 831, change:
```go
		assert.Equal(t, []string{"key-one", "key-two", "static-key"}, config.RequiredAPIKeys)
```
to:
```go
		assert.Equal(t, []APIKeyEntry{{Key: "key-one"}, {Key: "key-two"}, {Key: "static-key"}}, config.RequiredAPIKeys)
```

Line 1408, change:
```go
		assert.Equal(t, []string{"active-key-value"}, config.RequiredAPIKeys)
```
to:
```go
		assert.Equal(t, []APIKeyEntry{{Key: "active-key-value"}}, config.RequiredAPIKeys)
```

Line 1438, change:
```go
		assert.Equal(t, []string{"real-value"}, config.RequiredAPIKeys)
```
to:
```go
		assert.Equal(t, []APIKeyEntry{{Key: "real-value"}}, config.RequiredAPIKeys)
```

- [ ] **Step 7: Update `internal/server/auth.go`'s key-comparison loop**

Change (lines 18-33):

```go
func CreateAuthMiddleware(cfg config.Config) chain.Middleware {
	keys := cfg.RequiredAPIKeys
	return func(next http.Handler) http.Handler {
		if len(keys) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := extractAPIKey(r)

			valid := false
			for _, key := range keys {
				if provided == key {
					valid = true
					break
				}
			}
```

to:

```go
func CreateAuthMiddleware(cfg config.Config) chain.Middleware {
	keys := cfg.RequiredAPIKeys
	return func(next http.Handler) http.Handler {
		if len(keys) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := extractAPIKey(r)

			valid := false
			for _, entry := range keys {
				if provided != "" && provided == entry.Key {
					valid = true
					break
				}
			}
```

(Task 5 replaces this whole function with `CreateGlobalAuthMiddleware`; this step keeps `auth_test.go`'s existing `TestServer_AuthMiddleware` compiling and passing in the meantime.)

- [ ] **Step 8: Update `internal/server/auth_test.go`'s fixture**

Change line 94:

```go
	cfg := config.Config{RequiredAPIKeys: []string{"secret"}}
```

to:

```go
	cfg := config.Config{RequiredAPIKeys: []config.APIKeyEntry{{Key: "secret"}}}
```

- [ ] **Step 9: Run the full config and server test suites**

Run: `go test ./internal/config/... ./internal/server/... -v`
Expected: PASS — no compile errors, all existing and new tests green.

- [ ] **Step 10: gofmt and commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go internal/server/auth.go internal/server/auth_test.go
git add internal/config/config.go internal/config/config_test.go internal/server/auth.go internal/server/auth_test.go
git commit -m "$(cat <<'EOF'
config: upgrade apiKeys entries to carry id/label/createdAt

RequiredAPIKeys changes from []string to []APIKeyEntry so the upcoming
Settings UI can show human-readable, deletable key entries. A custom
UnmarshalYAML keeps existing bare-string apiKeys: entries working
unchanged (they parse as {Key: value}, with no id/label/createdAt).
EOF
)"
```

---

### Task 3: Add `auth.username`/`auth.password` config

**Files:**
- Modify: `internal/config/config.go` (add `AuthConfig` type + `Auth` field + validation)
- Test: `internal/config/config_test.go` (new tests)

**Interfaces:**
- Consumes: none beyond stdlib/yaml.
- Produces (used by Task 5, 6): `config.AuthConfig{Username, Password string}`, `Config.Auth AuthConfig`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
func TestConfig_Auth_ParsesUsernameAndPassword(t *testing.T) {
	content := `
auth:
  username: "admin"
  password: "hunter2"
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	cfg, err := LoadConfigFromReader(strings.NewReader(content))
	require.NoError(t, err)
	assert.Equal(t, "admin", cfg.Auth.Username)
	assert.Equal(t, "hunter2", cfg.Auth.Password)
}

func TestConfig_Auth_DefaultsToDisabled(t *testing.T) {
	content := `
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	cfg, err := LoadConfigFromReader(strings.NewReader(content))
	require.NoError(t, err)
	assert.Empty(t, cfg.Auth.Username)
	assert.Empty(t, cfg.Auth.Password)
}

func TestConfig_Auth_UsernameWithoutPasswordIsAnError(t *testing.T) {
	content := `
auth:
  username: "admin"
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.username and auth.password must both be set")
}

func TestConfig_Auth_PasswordWithoutUsernameIsAnError(t *testing.T) {
	content := `
auth:
  password: "hunter2"
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	_, err := LoadConfigFromReader(strings.NewReader(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.username and auth.password must both be set")
}

func TestConfig_Auth_EnvMacroSubstitution(t *testing.T) {
	t.Setenv("TEST_AUTH_PASSWORD", "from-env")
	content := `
auth:
  username: "admin"
  password: "${env.TEST_AUTH_PASSWORD}"
models:
  test:
    cmd: "server"
    proxy: "http://localhost:8080"
`
	cfg, err := LoadConfigFromReader(strings.NewReader(content))
	require.NoError(t, err)
	assert.Equal(t, "from-env", cfg.Auth.Password)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run TestConfig_Auth -v`
Expected: FAIL to compile — `cfg.Auth` doesn't exist yet.

- [ ] **Step 3: Add `AuthConfig` and the `Auth` field**

In `internal/config/config.go`, add this type near `HooksConfig` (just above `type Config struct {`):

```go
// AuthConfig gates every route llama-swap serves with HTTP Basic Auth. Empty
// Username and Password (the default) disables it.
type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}
```

Add the field to `Config` (near `RequiredAPIKeys`):

```go
	// gates every route with HTTP Basic Auth; empty means disabled
	Auth AuthConfig `yaml:"auth"`
```

- [ ] **Step 4: Add validation**

In `LoadConfigFromReader`, right after the API keys validation loop added in Task 2 Step 4, add:

```go
	// auth.username and auth.password must both be set or both empty —
	// a lone one is almost certainly a typo and would otherwise silently
	// lock everyone out (password set, username empty never matches) or
	// leave the server unprotected (username set, password empty never
	// matches either, since BasicAuth requires both).
	if (config.Auth.Username == "") != (config.Auth.Password == "") {
		return Config{}, fmt.Errorf("auth.username and auth.password must both be set, or both empty to disable protection")
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/... -run TestConfig_Auth -v`
Expected: PASS (all 5 new tests).

- [ ] **Step 6: Run the full config test suite**

Run: `go test ./internal/config/... -v`
Expected: PASS, no regressions.

- [ ] **Step 7: gofmt and commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
config: add auth.username/password for global Basic Auth

Empty username and password (the default) disables protection, matching
apiKeys' existing default-allow behavior. Setting only one of the two is
rejected at load time since it's almost certainly a typo that would
otherwise silently misbehave.
EOF
)"
```

---

### Task 4: Runtime config-file mutation for API keys and auth credentials

**Files:**
- Create: `internal/config/settingsedit.go`
- Test: `internal/config/settingsedit_test.go`

**Interfaces:**
- Consumes: `config.EditConfig`, `config.MappingChild`, `config.SequenceChild`, `config.StrNode` (Task 1).
- Produces (used by Task 6): `config.AddAPIKey(configPath, label string) (id, key string, err error)`, `config.RemoveAPIKey(configPath, id string) (bool, error)`, `config.SetAuthCredentials(configPath, username, password string) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/settingsedit_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSettingsTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("healthCheckTimeout: 600\n"), 0644))
	return path
}

func TestSettingsEdit_AddAPIKey(t *testing.T) {
	path := writeSettingsTestConfig(t)

	id, key, err := AddAPIKey(path, "CI pipeline")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.True(t, strings.HasPrefix(key, "sk-"))
	assert.Greater(t, len(key), 20)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 1)
	assert.Equal(t, id, cfg.RequiredAPIKeys[0].ID)
	assert.Equal(t, key, cfg.RequiredAPIKeys[0].Key)
	assert.Equal(t, "CI pipeline", cfg.RequiredAPIKeys[0].Label)
	assert.NotEmpty(t, cfg.RequiredAPIKeys[0].CreatedAt)
}

func TestSettingsEdit_AddAPIKey_NoLabel(t *testing.T) {
	path := writeSettingsTestConfig(t)

	_, _, err := AddAPIKey(path, "")
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 1)
	assert.Empty(t, cfg.RequiredAPIKeys[0].Label)
}

func TestSettingsEdit_AddAPIKey_GeneratesUniqueKeysAndIDs(t *testing.T) {
	path := writeSettingsTestConfig(t)

	id1, key1, err := AddAPIKey(path, "one")
	require.NoError(t, err)
	id2, key2, err := AddAPIKey(path, "two")
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, key1, key2)
}

func TestSettingsEdit_RemoveAPIKey(t *testing.T) {
	path := writeSettingsTestConfig(t)
	id, _, err := AddAPIKey(path, "to-remove")
	require.NoError(t, err)

	found, err := RemoveAPIKey(path, id)
	require.NoError(t, err)
	assert.True(t, found)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.RequiredAPIKeys)
}

func TestSettingsEdit_RemoveAPIKey_NotFound(t *testing.T) {
	path := writeSettingsTestConfig(t)
	found, err := RemoveAPIKey(path, "nonexistent-id")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSettingsEdit_RemoveAPIKey_LeavesOtherEntriesIntact(t *testing.T) {
	path := writeSettingsTestConfig(t)
	keepID, _, err := AddAPIKey(path, "keep")
	require.NoError(t, err)
	removeID, _, err := AddAPIKey(path, "remove")
	require.NoError(t, err)

	found, err := RemoveAPIKey(path, removeID)
	require.NoError(t, err)
	assert.True(t, found)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 1)
	assert.Equal(t, keepID, cfg.RequiredAPIKeys[0].ID)
}

func TestSettingsEdit_SetAuthCredentials(t *testing.T) {
	path := writeSettingsTestConfig(t)

	err := SetAuthCredentials(path, "admin", "hunter2")
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "admin", cfg.Auth.Username)
	assert.Equal(t, "hunter2", cfg.Auth.Password)
}

func TestSettingsEdit_SetAuthCredentials_EmptyDisables(t *testing.T) {
	path := writeSettingsTestConfig(t)
	require.NoError(t, SetAuthCredentials(path, "admin", "hunter2"))

	err := SetAuthCredentials(path, "", "")
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Auth.Username)
	assert.Empty(t, cfg.Auth.Password)
}

func TestSettingsEdit_ConcurrentAddAPIKey(t *testing.T) {
	path := writeSettingsTestConfig(t)

	var wg sync.WaitGroup
	ids := make([]string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, _, err := AddAPIKey(path, "")
			assert.NoError(t, err)
			ids[n] = id
		}(i)
	}
	wg.Wait()

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 5, "expected all 5 concurrent AddAPIKey calls to survive")
	seen := make(map[string]bool)
	for _, entry := range cfg.RequiredAPIKeys {
		assert.False(t, seen[entry.ID], "duplicate id %q", entry.ID)
		seen[entry.ID] = true
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/... -run TestSettingsEdit -v`
Expected: FAIL to compile — `AddAPIKey`, `RemoveAPIKey`, `SetAuthCredentials` are undefined.

- [ ] **Step 3: Implement `internal/config/settingsedit.go`**

```go
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"gopkg.in/yaml.v3"
)

var errNoSettingsChange = errors.New("no change")

// AddAPIKey generates a new random API key and a separate non-secret id,
// appends {id, key, label, createdAt} under apiKeys:, and returns the id and
// the plaintext key. The key is not stored anywhere else in plaintext after
// this call returns; callers must surface it to the operator immediately.
func AddAPIKey(configPath, label string) (id string, key string, err error) {
	keyBytes := make([]byte, 32)
	if _, err = rand.Read(keyBytes); err != nil {
		return "", "", err
	}
	key = "sk-" + base64.RawURLEncoding.EncodeToString(keyBytes)

	idBytes := make([]byte, 6)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", err
	}
	id = hex.EncodeToString(idBytes)

	createdAt := time.Now().UTC().Format(time.RFC3339)

	err = EditConfig(configPath, func(root *yaml.Node) error {
		keys, err := SequenceChild(root, "apiKeys")
		if err != nil {
			return err
		}
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content,
			StrNode("id", 0), StrNode(id, 0),
			StrNode("key", 0), StrNode(key, yaml.DoubleQuotedStyle),
		)
		if label != "" {
			entry.Content = append(entry.Content, StrNode("label", 0), StrNode(label, yaml.DoubleQuotedStyle))
		}
		entry.Content = append(entry.Content, StrNode("createdAt", 0), StrNode(createdAt, 0))
		keys.Content = append(keys.Content, entry)
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return id, key, nil
}

// RemoveAPIKey deletes the apiKeys: entry with the given id. Returns false
// if no entry has that id — this includes legacy bare-string entries, which
// have no id and so can only be removed by hand-editing the file.
func RemoveAPIKey(configPath, id string) (bool, error) {
	found := false
	err := EditConfig(configPath, func(root *yaml.Node) error {
		keys, err := SequenceChild(root, "apiKeys")
		if err != nil {
			return err
		}
		for i, item := range keys.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(item.Content); j += 2 {
				if item.Content[j].Value == "id" && item.Content[j+1].Value == id {
					keys.Content = append(keys.Content[:i], keys.Content[i+1:]...)
					found = true
					return nil
				}
			}
		}
		return errNoSettingsChange
	})
	if errors.Is(err, errNoSettingsChange) {
		return false, nil
	}
	return found, err
}

// SetAuthCredentials writes (or, if both are empty, clears) the top-level
// auth: mapping.
func SetAuthCredentials(configPath, username, password string) error {
	return EditConfig(configPath, func(root *yaml.Node) error {
		auth, err := MappingChild(root, "auth")
		if err != nil {
			return err
		}
		auth.Content = nil
		if username != "" || password != "" {
			auth.Content = append(auth.Content,
				StrNode("username", 0), StrNode(username, yaml.DoubleQuotedStyle),
				StrNode("password", 0), StrNode(password, yaml.DoubleQuotedStyle),
			)
		}
		return nil
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/... -run TestSettingsEdit -v`
Expected: PASS (all 9 new tests).

- [ ] **Step 5: Run the full config package test suite**

Run: `go test ./internal/config/... -v`
Expected: PASS, no regressions.

- [ ] **Step 6: gofmt and commit**

```bash
gofmt -w internal/config/settingsedit.go internal/config/settingsedit_test.go
git add internal/config/settingsedit.go internal/config/settingsedit_test.go
git commit -m "$(cat <<'EOF'
config: add runtime API key generation and auth credential writes

AddAPIKey/RemoveAPIKey/SetAuthCredentials let the Settings UI mutate
config.yaml at runtime through the same shared EditConfig lock the hub
feature uses, instead of requiring hand-edited YAML.
EOF
)"
```

---

### Task 5: Replace `CreateAuthMiddleware` with `CreateGlobalAuthMiddleware`

**Files:**
- Modify: `internal/server/auth.go`
- Modify: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: `config.Config.RequiredAPIKeys`, `config.Config.Auth` (Tasks 2, 3).
- Produces (used by Task 7): `server.CreateGlobalAuthMiddleware(cfg config.Config) chain.Middleware`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/server/auth_test.go` (these replace `TestServer_AuthMiddleware`, which is deleted in Step 4):

```go
func TestServer_GlobalAuthMiddleware(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("auth headers leaked to upstream")
		}
		w.WriteHeader(http.StatusOK)
	})

	t.Run("nothing configured passes through", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(config.Config{})
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ui/", nil))
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	apiKeyCfg := config.Config{RequiredAPIKeys: []config.APIKeyEntry{{Key: "secret"}}}

	t.Run("valid api key, no username/password configured", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(apiKeyCfg)
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("api key protects the UI route too", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(apiKeyCfg)
		r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 for unauthenticated /ui/ request", w.Code)
		}
	})

	authCfg := config.Config{Auth: config.AuthConfig{Username: "admin", Password: "hunter2"}}

	t.Run("valid basic auth, no api keys configured", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(authCfg)
		r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
		r.SetBasicAuth("admin", "hunter2")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("invalid basic auth password rejected", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(authCfg)
		r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
		r.SetBasicAuth("admin", "wrong")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Error("missing WWW-Authenticate header")
		}
	})

	bothCfg := config.Config{
		RequiredAPIKeys: []config.APIKeyEntry{{Key: "secret"}},
		Auth:            config.AuthConfig{Username: "admin", Password: "hunter2"},
	}

	t.Run("both configured: api key alone is sufficient", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(bothCfg)
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("x-api-key", "secret")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("both configured: basic auth alone is sufficient", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(bothCfg)
		r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
		r.SetBasicAuth("admin", "hunter2")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("both configured: neither credential rejected", func(t *testing.T) {
		mw := CreateGlobalAuthMiddleware(bothCfg)
		r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/... -run TestServer_GlobalAuthMiddleware -v`
Expected: FAIL to compile — `CreateGlobalAuthMiddleware` is undefined.

- [ ] **Step 3: Add `CreateGlobalAuthMiddleware` in `auth.go`, alongside the existing `CreateAuthMiddleware`**

In `internal/server/auth.go`, add this new function after the existing `CreateAuthMiddleware` function — do **not** remove or modify `CreateAuthMiddleware` in this task. It still has a caller in `server.go` (`routes()`'s `apiChain`); Task 7 removes both the old function and its caller together when it rewires `routes()` to use `CreateGlobalAuthMiddleware` instead. Keeping both functions side by side for one task means this commit compiles and every test passes, instead of leaving the package in a broken state until Task 7 lands.

```go
// CreateGlobalAuthMiddleware returns middleware that gates every request
// llama-swap serves — the UI, health/metrics endpoints, and inference/API
// routes alike — when either apiKeys or auth.username/password is
// configured. A request is admitted if it presents *either* a valid API key
// (Authorization: Bearer, Authorization: Basic password field, or
// x-api-key — unchanged from before) *or* valid HTTP Basic Auth
// username/password. When neither is configured it is a pass-through
// (today's default-allow behavior). On success the auth headers are
// stripped so they never leak to upstream.
func CreateGlobalAuthMiddleware(cfg config.Config) chain.Middleware {
	keys := cfg.RequiredAPIKeys
	username := cfg.Auth.Username
	password := cfg.Auth.Password
	authEnabled := username != "" || password != ""

	return func(next http.Handler) http.Handler {
		if len(keys) == 0 && !authEnabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := extractAPIKey(r)
			for _, entry := range keys {
				if provided != "" && provided == entry.Key {
					r.Header.Del("Authorization")
					r.Header.Del("x-api-key")
					next.ServeHTTP(w, r)
					return
				}
			}

			if authEnabled {
				if u, p, ok := r.BasicAuth(); ok &&
					subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1 &&
					subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1 {
					r.Header.Del("Authorization")
					r.Header.Del("x-api-key")
					next.ServeHTTP(w, r)
					return
				}
			}

			w.Header().Set("WWW-Authenticate", `Basic realm="llama-swap"`)
			router.SendResponse(w, r, http.StatusUnauthorized, "unauthorized: invalid or missing credentials")
		})
	}
}
```

Add `"crypto/subtle"` to the import block at the top of the file (keep the existing imports — `CreateAuthMiddleware` still needs them):

```go
import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/chain"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/router"
)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/server/... -run TestServer_GlobalAuthMiddleware -v`
Expected: PASS (all 8 subtests).

- [ ] **Step 5: Run the full server package test suite**

Run: `go test ./internal/server/... -v`
Expected: PASS — both `CreateAuthMiddleware` (still wired into `server.go`'s `apiChain`, untouched by this task) and the new `CreateGlobalAuthMiddleware` (not yet wired anywhere) coexist. No regressions in any existing test.

- [ ] **Step 6: gofmt and commit**

```bash
gofmt -w internal/server/auth.go internal/server/auth_test.go
git add internal/server/auth.go internal/server/auth_test.go
git commit -m "$(cat <<'EOF'
server: add CreateGlobalAuthMiddleware alongside CreateAuthMiddleware

Same API-key extraction as the existing CreateAuthMiddleware, plus an
alternate HTTP Basic Auth username/password check using a constant-time
comparison. Added side by side with the old function for now — Task 7
wires this at the outermost layer of the handler chain (so it covers
every route, not just modelChain/apiChain) and removes the now-unused
CreateAuthMiddleware in the same commit that removes its last caller.
EOF
)"
```

---

### Task 6: `Server.configPath` + `/api/settings/*` handlers

**Files:**
- Modify: `internal/server/server.go` (add `configPath` field + `SetConfigPath` setter)
- Create: `internal/server/settingsapi.go`
- Test: `internal/server/settingsapi_test.go`

**Interfaces:**
- Consumes: `config.AddAPIKey`, `config.RemoveAPIKey`, `config.SetAuthCredentials` (Task 4); `router.SendResponse` (existing).
- Produces (used by Task 7): `(*Server).SetConfigPath(path string)`, `(*Server).handleSettingsAuthGet`, `(*Server).handleSettingsAuthSet`, `(*Server).handleSettingsAPIKeysList`, `(*Server).handleSettingsAPIKeyGenerate`, `(*Server).handleSettingsAPIKeyDelete` (all `http.HandlerFunc`-compatible methods).

- [ ] **Step 1: Write the failing tests**

Create `internal/server/settingsapi_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSettingsTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("healthCheckTimeout: 600\n"), 0644))
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.cfg = cfg
	s.configPath = path
	return s
}

func TestSettingsAPI_AuthGet_Disabled(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	w := httptest.NewRecorder()
	s.handleSettingsAuthGet(w, httptest.NewRequest(http.MethodGet, "/api/settings/auth", nil))

	var body struct {
		Enabled  bool   `json:"enabled"`
		Username string `json:"username"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.False(t, body.Enabled)
	assert.Empty(t, body.Username)
}

func TestSettingsAPI_AuthGet_Enabled(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{Auth: config.AuthConfig{Username: "admin", Password: "hunter2"}})
	w := httptest.NewRecorder()
	s.handleSettingsAuthGet(w, httptest.NewRequest(http.MethodGet, "/api/settings/auth", nil))

	var body struct {
		Enabled  bool   `json:"enabled"`
		Username string `json:"username"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body.Enabled)
	assert.Equal(t, "admin", body.Username)
	assert.NotContains(t, w.Body.String(), "hunter2")
}

func TestSettingsAPI_AuthSet_RequiresBothOrNeither(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/auth", strings.NewReader(`{"username":"admin","password":""}`))
	w := httptest.NewRecorder()
	s.handleSettingsAuthSet(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsAPI_AuthSet_PersistsToConfigFile(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/auth", strings.NewReader(`{"username":"admin","password":"hunter2"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAuthSet(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	cfg, err := config.LoadConfig(s.configPath)
	require.NoError(t, err)
	assert.Equal(t, "admin", cfg.Auth.Username)
	assert.Equal(t, "hunter2", cfg.Auth.Password)
}

func TestSettingsAPI_APIKeysList_MasksKeys(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{
		RequiredAPIKeys: []config.APIKeyEntry{
			{ID: "abc123", Key: "sk-averylongsecretvalue1234567890", Label: "CI", CreatedAt: "2026-07-06T10:00:00Z"},
		},
	})
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeysList(w, httptest.NewRequest(http.MethodGet, "/api/settings/apikeys", nil))

	var body []struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		CreatedAt string `json:"createdAt"`
		MaskedKey string `json:"maskedKey"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Len(t, body, 1)
	assert.Equal(t, "abc123", body[0].ID)
	assert.Equal(t, "CI", body[0].Label)
	assert.NotContains(t, w.Body.String(), "sk-averylongsecretvalue1234567890")
	assert.Contains(t, body[0].MaskedKey, "…")
}

func TestSettingsAPI_APIKeyGenerate_PersistsAndReturnsPlaintextOnce(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", strings.NewReader(`{"label":"my key"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		ID    string `json:"id"`
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.NotEmpty(t, body.ID)
	assert.True(t, strings.HasPrefix(body.Key, "sk-"))
	assert.Equal(t, "my key", body.Label)

	cfg, err := config.LoadConfig(s.configPath)
	require.NoError(t, err)
	require.Len(t, cfg.RequiredAPIKeys, 1)
	assert.Equal(t, body.ID, cfg.RequiredAPIKeys[0].ID)
}

func TestSettingsAPI_APIKeyGenerate_NoBodyIsFine(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", nil)
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSettingsAPI_APIKeyDelete(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	genReq := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", strings.NewReader(`{}`))
	genW := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(genW, genReq)
	var genBody struct{ ID string `json:"id"` }
	require.NoError(t, json.NewDecoder(genW.Body).Decode(&genBody))

	delReq := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/delete", strings.NewReader(`{"id":"`+genBody.ID+`"}`))
	delW := httptest.NewRecorder()
	s.handleSettingsAPIKeyDelete(delW, delReq)
	assert.Equal(t, http.StatusOK, delW.Code)

	cfg, err := config.LoadConfig(s.configPath)
	require.NoError(t, err)
	assert.Empty(t, cfg.RequiredAPIKeys)
}

func TestSettingsAPI_APIKeyDelete_NotFound(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/delete", strings.NewReader(`{"id":"nope"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyDelete(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/... -run TestSettingsAPI -v`
Expected: FAIL to compile — `s.configPath`, `s.handleSettingsAuthGet`, etc. are undefined.

- [ ] **Step 3: Add the `configPath` field and setter to `Server`**

In `internal/server/server.go`, add the field to the `Server` struct (next to the `hub *hub.Manager` field):

```go
	hub *hub.Manager

	// configPath is the config.yaml path, used by /api/settings/* handlers
	// to persist API key and auth credential changes. Empty when the server
	// was constructed without SetConfigPath (e.g. in tests that don't need
	// settings persistence).
	configPath string
```

Add the setter right after `SetHubManager`:

```go
// SetConfigPath records the config.yaml path so /api/settings/* handlers can
// persist changes. Call this on every server built, before it starts
// serving, mirroring SetHubManager.
func (s *Server) SetConfigPath(path string) {
	s.configPath = path
}
```

- [ ] **Step 4: Implement `internal/server/settingsapi.go`**

```go
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/router"
)

// settingsEnabled gates every /api/settings write endpoint: persisting
// changes requires knowing where config.yaml lives.
func (s *Server) settingsEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.configPath == "" {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"settings management is unavailable: no config file path")
		return false
	}
	return true
}

func maskAPIKey(key string) string {
	if len(key) <= 10 {
		return "••••••"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

func (s *Server) handleSettingsAuthGet(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, map[string]any{
		"enabled":  s.cfg.Auth.Username != "" || s.cfg.Auth.Password != "",
		"username": s.cfg.Auth.Username,
	})
}

func (s *Server) handleSettingsAuthSet(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with username and password")
		return
	}
	if (req.Username == "") != (req.Password == "") {
		router.SendResponse(w, r, http.StatusBadRequest, "username and password must both be set, or both empty to disable protection")
		return
	}
	if err := config.SetAuthCredentials(s.configPath, req.Username, req.Password); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to save credentials: %v", err))
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}

func (s *Server) handleSettingsAPIKeysList(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		CreatedAt string `json:"createdAt"`
		MaskedKey string `json:"maskedKey"`
	}
	out := make([]entry, 0, len(s.cfg.RequiredAPIKeys))
	for _, k := range s.cfg.RequiredAPIKeys {
		out = append(out, entry{ID: k.ID, Label: k.Label, CreatedAt: k.CreatedAt, MaskedKey: maskAPIKey(k.Key)})
	}
	sendJSON(w, out)
}

func (s *Server) handleSettingsAPIKeyGenerate(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			router.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	label := strings.TrimSpace(req.Label)

	id, key, err := config.AddAPIKey(s.configPath, label)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to save API key: %v", err))
		return
	}
	sendJSON(w, map[string]string{"id": id, "key": key, "label": label})
}

func (s *Server) handleSettingsAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with id")
		return
	}
	found, err := config.RemoveAPIKey(s.configPath, req.ID)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to update config: %v", err))
		return
	}
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "no API key with that id")
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/server/... -run TestSettingsAPI -v`
Expected: PASS (all 9 tests). Note: `go test ./internal/server/...` as a whole still fails to build until Task 7 fixes `server.go`'s call to the now-removed `CreateAuthMiddleware` — that's expected and resolved in the next task.

- [ ] **Step 6: gofmt and commit**

```bash
gofmt -w internal/server/server.go internal/server/settingsapi.go internal/server/settingsapi_test.go
git add internal/server/server.go internal/server/settingsapi.go internal/server/settingsapi_test.go
git commit -m "$(cat <<'EOF'
server: add /api/settings handlers for auth + API key management

handleSettingsAuthGet/Set and handleSettingsAPIKeys{List,Generate,Delete}
back the new Settings UI. Server.configPath (set via SetConfigPath,
mirroring SetHubManager) tells them where to persist changes.

Known-broken until the next task: server.go still calls the removed
CreateAuthMiddleware, so `go build ./...` fails on this commit alone.
EOF
)"
```

---

### Task 7: Wire the global auth middleware and settings routes into `server.go` + `llama-swap.go`

**Files:**
- Modify: `internal/server/server.go` (`routes()` method)
- Modify: `llama-swap.go` (both `server.New` call sites)
- Test: `internal/server/server_test.go` (new integration tests)

**Interfaces:**
- Consumes: `CreateGlobalAuthMiddleware` (Task 5), the five `handleSettings*` methods and `SetConfigPath` (Task 6).

- [ ] **Step 1: Write the failing integration tests**

Add to `internal/server/server_test.go`:

```go
func TestServer_GlobalAuth_ProtectsUIAndMetrics(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "ok")
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Auth: config.AuthConfig{Username: "admin", Password: "hunter2"}}
	s.routes()

	for _, path := range []string{"/ui/", "/metrics", "/health"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s unauthenticated: status = %d, want 401", path, w.Code)
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.SetBasicAuth("admin", "hunter2")
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /health authenticated: status = %d, want 200", w.Code)
	}
}

func TestServer_GlobalAuth_DisabledByDefaultPassesThrough(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.routes()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestServer_SettingsRoutes_Registered(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.routes()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings/auth", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/settings/auth: status = %d, want 200", w.Code)
	}

	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/settings/apikeys", nil))
	if w2.Code != http.StatusOK {
		t.Errorf("GET /api/settings/apikeys: status = %d, want 200", w2.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/... -run 'TestServer_GlobalAuth|TestServer_SettingsRoutes' -v`
Expected: FAIL — `routes()` still wires the old `apiChain`/`authMW` setup, so `/ui/`, `/metrics`, and `/health` return 200 with no auth check, and `/api/settings/*` 404s (not yet registered).

- [ ] **Step 3: Rewrite `routes()` in `server.go`**

Replace the entire `routes()` method (from `func (s *Server) routes() {` through its closing `}`, i.e. the block currently at lines 183-266) with:

```go
// routes builds the mux, registers every route, and wraps the mux with the
// global request-log + CORS + auth middleware.
func (s *Server) routes() {
	filterMW := CreateFilterMiddleware(s.cfg)
	formFilterMW := CreateFormFilterMiddleware(s.cfg)

	// Model-dispatched routes get per-model concurrency limiting + body
	// filters + in-flight tracking + token metrics. Auth is handled once,
	// globally, by CreateGlobalAuthMiddleware in the outer handler below —
	// it now covers every route, not just this chain. concurrencyMW rejects
	// with 429 before the body filters do any rewrite work. filterMW
	// rewrites JSON bodies and formFilterMW rewrites multipart bodies; each
	// is a no-op for the other's Content-Type. Both run before the metrics
	// middleware so it buffers the rewritten body.
	modelChain := chain.New(
		CreateConcurrencyMiddleware(s.cfg),
		filterMW,
		formFilterMW,
		CreateInflightMiddleware(s.inflight),
		CreateMetricsMiddleware(s.metrics, s.cfg),
	)

	mux := http.NewServeMux()
	dispatch := http.HandlerFunc(s.localPeerHandler)

	for _, path := range modelPostJSONRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelPostFormRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelGetRoutes {
		mux.Handle("GET "+path, modelChain.Then(dispatch))
	}

	// llama-swap API + custom endpoints.
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("GET /logs", s.handleLogs)
	mux.HandleFunc("GET /logs/stream", s.handleLogStream)
	mux.HandleFunc("GET /logs/stream/{logMonitorID...}", s.handleLogStream)

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /wol-health", handleHealth)
	mux.HandleFunc("GET /{$}", handleRootRedirect)

	// Embedded UI.
	mux.HandleFunc("GET /ui/", s.handleUI)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	// Prometheus metrics.
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	// Operations endpoints.
	mux.HandleFunc("GET /unload", s.handleUnload)
	mux.HandleFunc("GET /running", s.handleRunning)

	// Upstream passthrough.
	mux.HandleFunc("GET /upstream", handleUpstreamRedirect)
	mux.HandleFunc("/upstream/{upstreamPath...}", s.handleUpstream)

	// API group consumed by the UI.
	mux.HandleFunc("POST /api/models/unload", s.handleAPIUnloadAll)
	mux.HandleFunc("POST /api/models/unload/{model...}", s.handleAPIUnloadModel)
	mux.HandleFunc("GET /api/events", s.handleAPIEvents)
	mux.HandleFunc("GET /api/metrics", s.handleAPIMetrics)
	mux.HandleFunc("GET /api/performance", s.handleAPIPerformance)
	mux.HandleFunc("GET /api/version", s.handleAPIVersion)
	mux.HandleFunc("GET /api/captures/{id}", s.handleAPICapture)

	// HuggingFace hub (model downloads).
	mux.HandleFunc("GET /api/hub/popular", s.handleHubPopular)
	mux.HandleFunc("GET /api/hub/search", s.handleHubSearch)
	mux.HandleFunc("GET /api/hub/repo/{repo...}", s.handleHubRepo)
	mux.HandleFunc("GET /api/hub/detail/{repo...}", s.handleHubDetail)
	mux.HandleFunc("GET /api/hub/downloads", s.handleHubDownloads)
	mux.HandleFunc("POST /api/hub/download", s.handleHubDownload)
	mux.HandleFunc("POST /api/hub/download/cancel", s.handleHubDownloadCancel)
	mux.HandleFunc("POST /api/hub/downloads/clear", s.handleHubDownloadsClear)
	mux.HandleFunc("POST /api/hub/delete", s.handleHubDelete)
	mux.HandleFunc("GET /api/hub/hardware", s.handleHubHardware)

	// Settings: auth credentials + API key management.
	mux.HandleFunc("GET /api/settings/auth", s.handleSettingsAuthGet)
	mux.HandleFunc("POST /api/settings/auth", s.handleSettingsAuthSet)
	mux.HandleFunc("GET /api/settings/apikeys", s.handleSettingsAPIKeysList)
	mux.HandleFunc("POST /api/settings/apikeys/generate", s.handleSettingsAPIKeyGenerate)
	mux.HandleFunc("POST /api/settings/apikeys/delete", s.handleSettingsAPIKeyDelete)

	s.mux = mux
	s.handler = chain.New(
		CreateRequestLogMiddleware(s.proxylog),
		CreateCORSMiddleware(), // must run before auth: answers OPTIONS preflight unauthenticated
		CreateGlobalAuthMiddleware(s.cfg),
	).Then(mux)
}
```

This removes the `apiChain` variable entirely: it previously existed only to apply `authMW`, which is now handled once, globally, at the outer `s.handler` layer, so every route that used to be wrapped in `apiChain.ThenFunc(...)` is now registered directly with plain `mux.HandleFunc(...)`.

- [ ] **Step 4: Remove the now-unused `CreateAuthMiddleware` and its test**

`routes()` no longer calls `CreateAuthMiddleware` — Task 5 added `CreateGlobalAuthMiddleware` alongside it rather than replacing it, specifically so this removal could happen in the same commit as its last caller disappearing. In `internal/server/auth.go`, delete the entire `CreateAuthMiddleware` function (and its doc comment) — everything from the comment block through its closing `}`, i.e. the function Task 5 left untouched. `extractAPIKey` and `CreateGlobalAuthMiddleware` remain.

In `internal/server/auth_test.go`, delete the entire `TestServer_AuthMiddleware` function — it tested `CreateAuthMiddleware`, which no longer exists, and is superseded by `TestServer_GlobalAuthMiddleware` (added in Task 5).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/server/... -run 'TestServer_GlobalAuth|TestServer_SettingsRoutes' -v`
Expected: PASS (all 3 new tests, including the sub-checks in `TestServer_GlobalAuth_ProtectsUIAndMetrics`).

- [ ] **Step 6: Run the full server package test suite**

Run: `go test ./internal/server/... -v`
Expected: PASS — confirm no regressions in any pre-existing test (`TestServer_CORSPreflight`, `TestServer_Health`, `TestServer_Unload`, etc.), and that removing `CreateAuthMiddleware`/`TestServer_AuthMiddleware` in Step 4 left no dangling references.

- [ ] **Step 7: Wire `configPath` through `llama-swap.go`**

In `llama-swap.go`, right after line 160 (`initialSrv.SetHubManager(hubManager)`), add:

```go
	initialSrv.SetConfigPath(configPath)
```

Right after line 216 (`newSrv.SetHubManager(hubManager)`, inside the `reload` closure), add:

```go
		newSrv.SetConfigPath(configPath)
```

- [ ] **Step 8: Build the whole project to confirm `llama-swap.go` compiles**

Run: `go build ./...`
Expected: succeeds with no errors.

- [ ] **Step 9: Run `make test-dev`**

Run: `make test-dev`
Expected: `go test` and `staticcheck` both pass with no errors across the whole repo.

- [ ] **Step 10: gofmt and commit**

```bash
gofmt -w internal/server/server.go internal/server/server_test.go internal/server/auth.go internal/server/auth_test.go llama-swap.go
git add internal/server/server.go internal/server/server_test.go internal/server/auth.go internal/server/auth_test.go llama-swap.go
git commit -m "$(cat <<'EOF'
server: apply global auth to every route, wire up settings routes

CreateGlobalAuthMiddleware now wraps the entire handler (UI, metrics,
health, inference, everything) instead of just modelChain/apiChain, so
apiChain's only job (applying authMW) is gone — routes it used to wrap
are now registered directly on the mux. The now-unused CreateAuthMiddleware
is removed in this same commit, alongside its superseded test. Also
registers the new /api/settings/* routes and threads configPath through
both server.New call sites in llama-swap.go so Settings-driven writes
know where config.yaml lives.
EOF
)"
```

---

### Task 8: Documentation — `docs/configuration.md`, `config-schema.json`, `README.md`

**Files:**
- Modify: `docs/configuration.md:215-226`
- Modify: `config-schema.json` (near the existing `apiKeys` property, line 437)
- Modify: `README.md:56`
- Modify: `config.example.yaml:137-149`

**Interfaces:** none (docs only).

- [ ] **Step 1: Update `docs/configuration.md`**

Replace the existing block (lines 215-226):

```
# apiKeys: require an API key when making requests to inference endpoints
# - optional, default: []
# - when empty (the default) authorization will not be checked as llama-swap is default-allow
# - each key is a non-empty string
apiKeys:
  - "sk-hunter2"
  # tip, one liner: printf "sk-%s\n" "$(head -c 48 /dev/urandom | base64 )"
  - "sk-gyCPiKUcIfPlaM4OSMZekkprgijPx6+OsmQs8Rsg0xZ9qpy6gKWsIKqHOk+cgXVx"

  # use environment variable macros to keep secrets out of the config
  - "${env.API_KEY_1}"
  - "${env.API_KEY_2}"
```

with:

```
# apiKeys: require an API key when making requests to inference endpoints
# - optional, default: []
# - when empty (the default) authorization will not be checked as llama-swap is default-allow
# - each entry is either a bare string, or a mapping with an optional label/createdAt
# - the Settings page in the UI can generate, label, and revoke these at runtime
apiKeys:
  - "sk-hunter2"
  # tip, one liner: printf "sk-%s\n" "$(head -c 48 /dev/urandom | base64 )"
  - "sk-gyCPiKUcIfPlaM4OSMZekkprgijPx6+OsmQs8Rsg0xZ9qpy6gKWsIKqHOk+cgXVx"

  # use environment variable macros to keep secrets out of the config
  - "${env.API_KEY_1}"
  - "${env.API_KEY_2}"

  # entries generated from the UI look like this; id is used for deletion,
  # label/createdAt are display-only
  - id: "a1b2c3d4e5f6"
    key: "sk-...."
    label: "CI pipeline"
    createdAt: "2026-07-06T10:00:00Z"

# auth: protect every route llama-swap serves (the UI dashboard, health,
# metrics, and inference endpoints alike) with HTTP Basic Auth.
# - optional, default: disabled
# - username and password must both be set, or both left empty to disable
# - a request is admitted if it presents either a valid apiKeys entry OR
#   valid auth credentials — existing apiKeys-based API clients are
#   unaffected by turning this on
# - the Settings page in the UI can configure this at runtime
auth:
  username: "admin"
  password: "hunter2"
```

- [ ] **Step 2: Update `config-schema.json`**

Change the existing `apiKeys` property (line 437-445):

```json
        "apiKeys": {
            "type": "array",
            "items": {
                "type": "string",
                "minLength": 1
            },
            "default": [],
            "description": "Require an API key when making requests to inference endpoints. When empty, authorization will not be checked. Each key is a non-empty string."
        },
```

to:

```json
        "apiKeys": {
            "type": "array",
            "items": {
                "oneOf": [
                    {
                        "type": "string",
                        "minLength": 1
                    },
                    {
                        "type": "object",
                        "required": ["key"],
                        "properties": {
                            "id": { "type": "string" },
                            "key": { "type": "string", "minLength": 1 },
                            "label": { "type": "string" },
                            "createdAt": { "type": "string" }
                        },
                        "additionalProperties": false
                    }
                ]
            },
            "default": [],
            "description": "Require an API key when making requests to any endpoint. When empty, authorization will not be checked. Each entry is either a non-empty string, or an object with a required 'key' and optional id/label/createdAt (as generated by the Settings UI)."
        },
        "auth": {
            "type": "object",
            "properties": {
                "username": { "type": "string" },
                "password": { "type": "string" }
            },
            "additionalProperties": false,
            "description": "Protect every route (UI, health, metrics, inference endpoints) with HTTP Basic Auth. username and password must both be set, or both left empty to disable. A valid apiKeys entry is always an accepted alternate credential."
        },
```

- [ ] **Step 3: Update `README.md`**

Change line 56:

```
- ✅ API Key support - define keys to restrict access to API endpoints
```

to:

```
- ✅ API Key support - define keys to restrict access to API endpoints, generate and manage them from the UI
- ✅ Username/password protection - lock down the entire UI and API with HTTP Basic Auth
```

- [ ] **Step 4: Update `config.example.yaml`**

Apply the same replacement as Step 1 to `config.example.yaml:137-149` (identical block, same file layout).

- [ ] **Step 5: Validate the JSON schema parses**

Run: `python3 -c "import json; json.load(open('config-schema.json'))"`
Expected: no output, exit code 0 (valid JSON).

- [ ] **Step 6: Commit**

```bash
git add docs/configuration.md config-schema.json README.md config.example.yaml
git commit -m "$(cat <<'EOF'
docs: document auth.username/password and the new apiKeys entry shape
EOF
)"
```

---

### Task 9: UI — types and API client functions

**Files:**
- Modify: `ui-svelte/src/lib/types.ts` (add `ApiKeyEntry`, `AuthStatus`)
- Modify: `ui-svelte/src/stores/api.ts` (add settings functions)

**Interfaces:**
- Produces (used by Task 10): `getAuthStatus(): Promise<AuthStatus>`, `setAuthCredentials(username: string, password: string): Promise<void>`, `listApiKeys(): Promise<ApiKeyEntry[]>`, `generateApiKey(label: string): Promise<{id: string; key: string; label: string}>`, `deleteApiKey(id: string): Promise<void>`.

- [ ] **Step 1: Add types to `ui-svelte/src/lib/types.ts`**

Add near the existing `HubFile` interface:

```ts
export interface AuthStatus {
  enabled: boolean;
  username: string;
}

export interface ApiKeyEntry {
  id: string;
  label: string;
  createdAt: string;
  maskedKey: string;
}
```

- [ ] **Step 2: Add functions to `ui-svelte/src/stores/api.ts`**

Add the new types to the existing import block at the top of the file:

```ts
import type {
  Model,
  ActivityLogEntry,
  VersionInfo,
  LogData,
  APIEventEnvelope,
  ReqRespCapture,
  InFlightStats,
  PerformanceResponse,
  DownloadInfo,
  HubRepo,
  HubRepoDetail,
  HubFile,
  AuthStatus,
  ApiKeyEntry,
} from "../lib/types";
```

Add these functions after `hubDeleteModel` (following the existing `hubFetch` helper):

```ts
export async function getAuthStatus(): Promise<AuthStatus> {
  return hubFetch<AuthStatus>("/api/settings/auth");
}

export async function setAuthCredentials(username: string, password: string): Promise<void> {
  await hubFetch("/api/settings/auth", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
}

export async function listApiKeys(): Promise<ApiKeyEntry[]> {
  return hubFetch<ApiKeyEntry[]>("/api/settings/apikeys");
}

export async function generateApiKey(label: string): Promise<{ id: string; key: string; label: string }> {
  return hubFetch(`/api/settings/apikeys/generate`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ label }),
  });
}

export async function deleteApiKey(id: string): Promise<void> {
  await hubFetch(`/api/settings/apikeys/delete`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id }),
  });
}
```

(These reuse the existing `hubFetch<T>` helper already defined in this file for the hub feature — it throws on non-OK responses with the response body as the message, which is exactly the behavior wanted here too.)

- [ ] **Step 3: Typecheck**

Run: `cd ui-svelte && npm run check`
Expected: no new type errors.

- [ ] **Step 4: Commit**

```bash
git add ui-svelte/src/lib/types.ts ui-svelte/src/stores/api.ts
git commit -m "$(cat <<'EOF'
ui: add API client functions for the Settings page

getAuthStatus/setAuthCredentials/listApiKeys/generateApiKey/deleteApiKey
call the new /api/settings/* backend endpoints, following the existing
hubFetch pattern used by the model-download feature.
EOF
)"
```

---

### Task 10: UI — Settings page, route, and nav link

**Files:**
- Create: `ui-svelte/src/routes/Settings.svelte`
- Modify: `ui-svelte/src/App.svelte` (register `/settings` route)
- Modify: `ui-svelte/src/components/Header.svelte` (add nav link)

**Interfaces:**
- Consumes: `getAuthStatus`, `setAuthCredentials`, `listApiKeys`, `generateApiKey`, `deleteApiKey` (Task 9).

- [ ] **Step 1: Create `ui-svelte/src/routes/Settings.svelte`**

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { getAuthStatus, setAuthCredentials, listApiKeys, generateApiKey, deleteApiKey } from "../stores/api";
  import type { AuthStatus, ApiKeyEntry } from "../lib/types";

  let authStatus = $state<AuthStatus>({ enabled: false, username: "" });
  let username = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let authError = $state("");
  let authSaving = $state(false);

  let apiKeys = $state<ApiKeyEntry[]>([]);
  let newKeyLabel = $state("");
  let generatedKey = $state("");
  let generatedKeyCopied = $state(false);
  let apiKeysError = $state("");

  async function loadAuthStatus(): Promise<void> {
    authStatus = await getAuthStatus();
    username = authStatus.username;
  }

  async function loadApiKeys(): Promise<void> {
    apiKeys = await listApiKeys();
  }

  onMount(() => {
    loadAuthStatus();
    loadApiKeys();
  });

  async function handleSaveAuth(): Promise<void> {
    authError = "";
    if (password !== confirmPassword) {
      authError = "Passwords do not match";
      return;
    }
    if (username === "" && password === "") {
      if (!confirm("Anyone will be able to access this server. Continue?")) {
        return;
      }
    }
    authSaving = true;
    try {
      await setAuthCredentials(username, password);
      password = "";
      confirmPassword = "";
      await loadAuthStatus();
    } catch (e) {
      authError = e instanceof Error ? e.message : String(e);
    } finally {
      authSaving = false;
    }
  }

  async function handleGenerateKey(): Promise<void> {
    apiKeysError = "";
    try {
      const result = await generateApiKey(newKeyLabel.trim());
      generatedKey = result.key;
      generatedKeyCopied = false;
      newKeyLabel = "";
      await loadApiKeys();
    } catch (e) {
      apiKeysError = e instanceof Error ? e.message : String(e);
    }
  }

  async function handleDeleteKey(entry: ApiKeyEntry): Promise<void> {
    const label = entry.label || entry.maskedKey;
    if (!confirm(`Delete API key "${label}"? Any client using it will lose access immediately.`)) {
      return;
    }
    apiKeysError = "";
    try {
      await deleteApiKey(entry.id);
      await loadApiKeys();
    } catch (e) {
      apiKeysError = e instanceof Error ? e.message : String(e);
    }
  }

  async function copyGeneratedKey(): Promise<void> {
    await navigator.clipboard.writeText(generatedKey);
    generatedKeyCopied = true;
    setTimeout(() => (generatedKeyCopied = false), 1500);
  }

  function dismissGeneratedKey(): void {
    generatedKey = "";
  }
</script>

<div class="flex flex-col gap-4 h-full overflow-auto">
  <div class="card">
    <h3 class="mb-2">Access Control</h3>
    <p class="text-sm text-txtsecondary mb-4">
      {#if authStatus.enabled}
        This server is protected. Visitors must sign in with the username/password below.
      {:else}
        This server is <strong>not protected</strong> — anyone with network access can use it.
      {/if}
    </p>

    <div class="flex flex-col gap-2 max-w-md">
      <label class="text-sm" for="settings-username">Username</label>
      <input
        id="settings-username"
        type="text"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={username}
        autocomplete="off"
      />

      <label class="text-sm" for="settings-password">New password</label>
      <input
        id="settings-password"
        type="password"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={password}
        placeholder={authStatus.enabled ? "leave blank to keep current password" : ""}
        autocomplete="new-password"
      />

      <label class="text-sm" for="settings-password-confirm">Confirm password</label>
      <input
        id="settings-password-confirm"
        type="password"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={confirmPassword}
        autocomplete="new-password"
      />

      {#if authError}
        <p class="text-error text-sm">{authError}</p>
      {/if}

      <button class="btn text-base self-start" onclick={handleSaveAuth} disabled={authSaving}>
        {authSaving ? "Saving…" : "Save"}
      </button>
    </div>
  </div>

  <div class="card">
    <h3 class="mb-2">API Keys</h3>
    <p class="text-sm text-txtsecondary mb-4">
      Keys grant programmatic access (curl, SDKs) as an alternative to the username/password above.
    </p>

    {#if generatedKey}
      <div class="mb-4 p-3 rounded border border-warning bg-warning/10">
        <p class="text-sm font-semibold mb-1">Copy this key now — it won't be shown again.</p>
        <div class="flex items-center gap-2">
          <code class="flex-1 break-all">{generatedKey}</code>
          <button class="btn btn--sm" onclick={copyGeneratedKey}>{generatedKeyCopied ? "Copied!" : "Copy"}</button>
          <button class="btn btn--sm" onclick={dismissGeneratedKey}>Dismiss</button>
        </div>
      </div>
    {/if}

    <div class="flex items-center gap-2 mb-4">
      <input
        type="text"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        placeholder="Label (optional)"
        bind:value={newKeyLabel}
      />
      <button class="btn text-base" onclick={handleGenerateKey}>+ Generate key</button>
    </div>

    {#if apiKeysError}
      <p class="text-error text-sm mb-2">{apiKeysError}</p>
    {/if}

    <table class="w-full text-sm">
      <thead>
        <tr class="text-left text-txtsecondary">
          <th>Label</th>
          <th>Key</th>
          <th>Created</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each apiKeys as entry (entry.id || entry.maskedKey)}
          <tr class="border-t border-card-border-inner">
            <td>{entry.label || "—"}</td>
            <td><code>{entry.maskedKey}</code></td>
            <td>{entry.createdAt ? new Date(entry.createdAt).toLocaleString() : "—"}</td>
            <td>
              {#if entry.id}
                <button class="btn btn--sm" onclick={() => handleDeleteKey(entry)}>Delete</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
```

- [ ] **Step 2: Register the route in `App.svelte`**

Add the import (after the `Playground`/`PlaygroundStub` imports):

```ts
  import Settings from "./routes/Settings.svelte";
```

Add the route entry to the `routes` map:

```ts
  const routes = {
    "/": PlaygroundStub,
    "/models": Models,
    "/logs": LogViewer,
    "/activity": Activity,
    "/performance": Performance,
    "/settings": Settings,
    "*": PlaygroundStub,
  };
```

- [ ] **Step 3: Add the nav link in `Header.svelte`**

Add, right after the existing "Performance" `<a>` link (before the theme toggle `<button>`):

```svelte
    <a
      href="/settings"
      use:link
      class="text-gray-600 hover:text-black dark:text-gray-300 dark:hover:text-gray-100 p-1 whitespace-nowrap"
      class:font-semibold={isActive("/settings", $currentRoute)}
      class:underline={isActive("/settings", $currentRoute)}
      class:underline-offset-4={isActive("/settings", $currentRoute)}
    >
      Settings
    </a>
```

- [ ] **Step 4: Typecheck and run the UI test suite**

Run: `cd ui-svelte && npm run check && npm test`
Expected: no type errors, all existing vitest suites still pass (no new tests are added in this task — `Settings.svelte` has no existing component-test precedent in this codebase, matching how `DiscoverPanel.svelte`/`ModelsPanel.svelte` etc. are also untested at the component level; only `lib/*.ts` pure-logic modules have vitest coverage).

- [ ] **Step 5: Manually verify in the browser**

Run: `cd ui-svelte && npm run dev` (or `make ui` + run the built binary), then:
1. Navigate to `/settings`. Confirm both cards render, "not protected" message shows.
2. Set a username/password, save. Confirm the browser's next request 401s and prompts for credentials, and that the saved credentials work.
3. Generate an API key with a label, confirm it displays once and copies to clipboard, then reload the page and confirm it now shows masked with a Delete button.
4. Delete the key, confirm it disappears from the list.
5. Clear username/password (with the confirm dialog), confirm the server becomes unprotected again.

- [ ] **Step 6: Commit**

```bash
git add ui-svelte/src/routes/Settings.svelte ui-svelte/src/App.svelte ui-svelte/src/components/Header.svelte
git commit -m "$(cat <<'EOF'
ui: add Settings page for access control and API key management

New /settings route with two cards: Access Control (username/password
form, wired to PUT /api/settings/auth) and API Keys (generate/label/
delete, wired to /api/settings/apikeys*). Nav link added to Header.
EOF
)"
```

---

### Task 11: Final verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full Go test suite with static analysis**

Run: `make test-dev`
Expected: `go test ./...` and `staticcheck ./...` both pass with zero failures/warnings.

- [ ] **Step 2: Run the full UI test suite**

Run: `make test-ui`
Expected: `npm ci && npm run check && npm test` all succeed.

- [ ] **Step 3: Run the long-running concurrency suite**

Run: `make test-all`
Expected: passes, including the concurrent-edit tests added in Tasks 1 and 4.

- [ ] **Step 4: Manually verify default (unconfigured) behavior is unchanged**

Run: `go run . --config config.example.yaml --listen :18080` (or equivalent build output) with `apiKeys` and `auth` both absent/empty in a scratch copy of the config, and confirm `curl -i http://localhost:18080/ui/` returns `200` with no `WWW-Authenticate` header — the default-allow behavior for users who configure neither feature must be byte-for-byte unchanged from before this plan.

- [ ] **Step 5: Confirm no fixup commits are needed**

Run: `git status`
Expected: clean working tree — every task's changes were already committed at the end of that task.
