package server

import (
	"encoding/json"
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

func TestHubAPI_Hardware(t *testing.T) {
	srv := newHubTestServer(t, "models: {}\n")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/hardware", nil))
	assert.Equal(t, http.StatusOK, w.Code)

	var hw struct {
		RamMB   int    `json:"ramMB"`
		VramMB  int    `json:"vramMB"`
		GpuName string `json:"gpuName"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hw))
	assert.Greater(t, hw.RamMB, 0)
	assert.GreaterOrEqual(t, hw.VramMB, 0)
}

func TestHubAPI_RejectsInvalidRepoName(t *testing.T) {
	srv := newHubTestServer(t, "modelsDir: /tmp\nmodels: {}\n")
	srv.SetHubManager(hub.NewManager(hub.Options{}))

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/repo/..%2Fadmin/x", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/hub/download",
		strings.NewReader(`{"repo":"org/name?x=1","file":"a.gguf"}`))
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHubAPI_DownloadsSnapshot(t *testing.T) {
	srv := newHubTestServer(t, "modelsDir: /tmp\nmodels: {}\n")
	srv.SetHubManager(hub.NewManager(hub.Options{}))

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/downloads", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}
