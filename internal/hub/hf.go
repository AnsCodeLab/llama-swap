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
