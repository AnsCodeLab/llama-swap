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
