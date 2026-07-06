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

// bootstrappedConfig returns a config with a pre-existing auth credential
// already set, i.e. the state a server must reach before the mutating
// /api/settings/* endpoints will do anything. Tests that exercise those
// mutating endpoints for behavior other than the credential gate itself
// (bad request handling, persistence, delete-not-found, etc.) need to start
// from this bootstrapped state so they aren't short-circuited by the 503.
func bootstrappedConfig() config.Config {
	return config.Config{Auth: config.AuthConfig{Username: "bootstrap-admin", Password: "bootstrap-pw"}}
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
	s := newSettingsTestServer(t, bootstrappedConfig())
	req := httptest.NewRequest(http.MethodPost, "/api/settings/auth", strings.NewReader(`{"username":"admin","password":""}`))
	w := httptest.NewRecorder()
	s.handleSettingsAuthSet(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsAPI_AuthSet_PersistsToConfigFile(t *testing.T) {
	s := newSettingsTestServer(t, bootstrappedConfig())
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
	s := newSettingsTestServer(t, bootstrappedConfig())
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
	s := newSettingsTestServer(t, bootstrappedConfig())
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", nil)
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSettingsAPI_APIKeyDelete(t *testing.T) {
	s := newSettingsTestServer(t, bootstrappedConfig())
	genReq := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", strings.NewReader(`{}`))
	genW := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(genW, genReq)
	var genBody struct {
		ID string `json:"id"`
	}
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
	s := newSettingsTestServer(t, bootstrappedConfig())
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/delete", strings.NewReader(`{"id":"nope"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyDelete(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Fix: mutating settings endpoints must require an existing credential ---
//
// Without this gate, a freshly installed server with neither apiKeys nor
// auth.username/password configured (the default, out-of-the-box state)
// would let any anonymous network visitor call these mutation endpoints,
// since the global auth middleware is a full pass-through until a credential
// exists. That would let an attacker plant a persistent API key or set their
// own auth credentials, potentially locking out the real operator.

func TestSettingsAPI_AuthSet_RequiresExistingCredential(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/auth", strings.NewReader(`{"username":"attacker","password":"pwned"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAuthSet(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	// Confirm the config file itself was left untouched.
	cfg, err := config.LoadConfig(s.configPath)
	require.NoError(t, err)
	assert.Empty(t, cfg.Auth.Username)
}

func TestSettingsAPI_APIKeyGenerate_RequiresExistingCredential(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", strings.NewReader(`{"label":"attacker key"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	cfg, err := config.LoadConfig(s.configPath)
	require.NoError(t, err)
	assert.Empty(t, cfg.RequiredAPIKeys)
}

func TestSettingsAPI_APIKeyDelete_RequiresExistingCredential(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/delete", strings.NewReader(`{"id":"whatever"}`))
	w := httptest.NewRecorder()
	s.handleSettingsAPIKeyDelete(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsAPI_MutatingEndpoints_WorkOnceCredentialExists(t *testing.T) {
	// Confirms the bootstrapped case (already covered individually by other
	// tests above) isn't broken by the new gate, exercising all three
	// mutating endpoints together against a config with a pre-existing
	// credential.
	s := newSettingsTestServer(t, bootstrappedConfig())

	authReq := httptest.NewRequest(http.MethodPost, "/api/settings/auth", strings.NewReader(`{"username":"admin","password":"newpw"}`))
	authW := httptest.NewRecorder()
	s.handleSettingsAuthSet(authW, authReq)
	assert.Equal(t, http.StatusOK, authW.Code)

	genReq := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/generate", strings.NewReader(`{"label":"ci"}`))
	genW := httptest.NewRecorder()
	s.handleSettingsAPIKeyGenerate(genW, genReq)
	require.Equal(t, http.StatusOK, genW.Code)

	var genBody struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(genW.Body).Decode(&genBody))

	delReq := httptest.NewRequest(http.MethodPost, "/api/settings/apikeys/delete", strings.NewReader(`{"id":"`+genBody.ID+`"}`))
	delW := httptest.NewRecorder()
	s.handleSettingsAPIKeyDelete(delW, delReq)
	assert.Equal(t, http.StatusOK, delW.Code)
}

func TestSettingsAPI_ReadOnlyEndpoints_NotGatedByMissingCredential(t *testing.T) {
	s := newSettingsTestServer(t, config.Config{})

	authW := httptest.NewRecorder()
	s.handleSettingsAuthGet(authW, httptest.NewRequest(http.MethodGet, "/api/settings/auth", nil))
	assert.Equal(t, http.StatusOK, authW.Code)

	listW := httptest.NewRecorder()
	s.handleSettingsAPIKeysList(listW, httptest.NewRequest(http.MethodGet, "/api/settings/apikeys", nil))
	assert.Equal(t, http.StatusOK, listW.Code)
}
