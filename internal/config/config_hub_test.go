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
