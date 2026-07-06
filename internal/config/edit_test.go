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
