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
		assert.Contains(t, r.URL.Query()["expand[]"], "lastModified")
		w.Write([]byte(`[{"id":"bartowski/Qwen-GGUF","downloads":1000,"likes":42,"lastModified":"2026-01-01T00:00:00.000Z","pipeline_tag":"text-generation"}]`))
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL})
	repos, err := m.Search(context.Background(), "tok", "qwen", 30)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "bartowski/Qwen-GGUF", repos[0].ID)
	assert.Equal(t, int64(1000), repos[0].Downloads)
	assert.Equal(t, int64(42), repos[0].Likes)
	assert.Equal(t, "2026-01-01T00:00:00.000Z", repos[0].LastModified)
	assert.Equal(t, "text-generation", repos[0].PipelineTag)
}

func TestHubManager_RepoDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/org/repo":
			w.Write([]byte(`{"id":"org/repo","author":"org","downloads":5,"likes":2,
				"lastModified":"2026-01-01T00:00:00.000Z","pipeline_tag":"text-generation",
				"tags":["gguf","llama"]}`))
		case "/org/repo/raw/main/README.md":
			w.Write([]byte("---\nlicense: mit\n---\n# Hello\n\nModel card body."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL})
	d, err := m.RepoDetail(context.Background(), "", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, "org/repo", d.ID)
	assert.Equal(t, "org", d.Author)
	assert.Equal(t, int64(5), d.Downloads)
	assert.Equal(t, "text-generation", d.PipelineTag)
	assert.Equal(t, []string{"gguf", "llama"}, d.Tags)
	assert.Equal(t, "# Hello\n\nModel card body.", d.Readme)
}

func TestHubManager_RepoDetailNoReadme(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/models/org/repo" {
			w.Write([]byte(`{"id":"org/repo"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL})
	d, err := m.RepoDetail(context.Background(), "", "org/repo")
	require.NoError(t, err)
	assert.Equal(t, "", d.Readme)
	assert.Equal(t, []string{}, d.Tags)
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
	assert.Equal(t, "model-F16.gguf", files[0].Name)
	assert.Equal(t, "F16", files[0].Quant)
	assert.False(t, files[0].Downloaded)
	assert.Equal(t, "model-Q4_K_M.gguf", files[1].Name)
	assert.Equal(t, "Q4_K_M", files[1].Quant)
	assert.True(t, files[1].Downloaded)
}

func TestHubManager_ListFilesSkipsUnsafeNames(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"siblings":[
			{"rfilename":"good-Q4.gguf","size":1},
			{"rfilename":"-starts-with-dash.gguf","size":1},
			{"rfilename":"has space.gguf","size":1},
			{"rfilename":"dollar${PORT}.gguf","size":1},
			{"rfilename":"quote\".gguf","size":1}
		]}`))
	}))
	defer ts.Close()

	m := NewManager(Options{BaseURL: ts.URL})
	files, err := m.ListFiles(context.Background(), "", "org/repo", "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "good-Q4.gguf", files[0].Name)
}

func TestHubManager_ValidRepoName(t *testing.T) {
	assert.True(t, ValidRepoName("bartowski/Qwen-GGUF"))
	assert.True(t, ValidRepoName("org-1.x/model_name.v2"))
	assert.False(t, ValidRepoName("noslash"))
	assert.False(t, ValidRepoName("a/b/c"))
	assert.False(t, ValidRepoName("../etc/passwd"))
	assert.False(t, ValidRepoName("org/name?x=1"))
	assert.False(t, ValidRepoName("org/name#frag"))
	assert.False(t, ValidRepoName("org/%2e%2e"))
	assert.False(t, ValidRepoName("-org/name"))
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
