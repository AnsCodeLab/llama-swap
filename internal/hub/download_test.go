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

	"github.com/mostlygeek/llama-swap/internal/shared"
)

// hubFileServer serves content at /org/repo/resolve/main/<name> with Range support.
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

func waitForState(t *testing.T, m *Manager, id, state string) shared.DownloadInfo {
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
