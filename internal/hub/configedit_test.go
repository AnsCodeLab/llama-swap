package hub

import (
	"os"
	"path/filepath"
	"sync"
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

func TestConfigEdit_AddModelEntryFlowStyleModels(t *testing.T) {
	// `models: {}` is flow style; new entries must still render block style
	// with a literal cmd scalar, not inline JSON-ish flow mappings.
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("models: {}\n"), 0644))
	err := AddModelEntry(path, "m1", "M1", "llama-server --port ${PORT} -m /models/m1.gguf")
	require.NoError(t, err)
	out, _ := os.ReadFile(path)
	s := string(out)
	assert.Contains(t, s, "cmd: |")
	assert.NotContains(t, s, `models: {"m1"`)
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

// TestConfigEdit_ConcurrentAddModelEntry guards against a regression where
// concurrent AddModelEntry calls (e.g. two hub downloads finishing around
// the same time) raced on editConfig's read-modify-write cycle: each read
// the pre-edit file, applied its own change in memory, then wrote back —
// last writer wins, silently discarding every other concurrent edit
// including unrelated pre-existing model entries the stale read never saw.
func TestConfigEdit_ConcurrentAddModelEntry(t *testing.T) {
	path := writeTestConfig(t)

	names := []string{"new-a", "new-b", "new-c", "new-d", "new-e"}
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			assert.NoError(t, AddModelEntry(path, name, name,
				"llama-server --port ${PORT} -m /models/"+name+".gguf"))
		}(n)
	}
	wg.Wait()

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(out)

	// the pre-existing hand-written entry must survive every concurrent edit
	assert.Contains(t, s, `"qwen3-4b":`)
	assert.Contains(t, s, "# hand-written entry, comments must survive edits")
	for _, n := range names {
		assert.Contains(t, s, `"`+n+`":`, "entry %q lost to a concurrent-write race", n)
	}
}
