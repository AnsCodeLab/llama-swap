package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	ID           string   `json:"id"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"lastModified"`
	PipelineTag  string   `json:"pipelineTag"`
	Tags         []string `json:"tags"`
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

	// repoNameRe matches HF "org/name" repo IDs. Anything else is rejected
	// before being interpolated into upstream API URLs.
	repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

	// safeFileNameRe constrains GGUF filenames that land on disk and inside
	// the generated config cmd (argv). No whitespace, quotes, macros, or a
	// leading "-" so a hostile repo cannot inject arguments.
	safeFileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.gguf$`)
)

// ValidRepoName reports whether repo is a well-formed "org/name" HF repo ID.
func ValidRepoName(repo string) bool {
	return repoNameRe.MatchString(repo)
}

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
	q["expand[]"] = []string{"downloads", "likes", "pipeline_tag", "lastModified", "tags"}
	if query != "" {
		q.Set("search", query)
	}
	var raw []struct {
		ID           string   `json:"id"`
		Downloads    int64    `json:"downloads"`
		Likes        int64    `json:"likes"`
		LastModified string   `json:"lastModified"`
		PipelineTag  string   `json:"pipeline_tag"`
		Tags         []string `json:"tags"`
	}
	if err := m.hfGet(ctx, token, "/api/models", q, &raw); err != nil {
		return nil, err
	}
	repos := make([]RepoSummary, 0, len(raw))
	for _, r := range raw {
		if r.Tags == nil {
			r.Tags = []string{}
		}
		repos = append(repos, RepoSummary{
			ID:           r.ID,
			Downloads:    r.Downloads,
			Likes:        r.Likes,
			LastModified: r.LastModified,
			PipelineTag:  r.PipelineTag,
			Tags:         r.Tags,
		})
	}
	return repos, nil
}

// RepoDetail is the metadata + model card shown in the UI's detail modal.
type RepoDetail struct {
	ID           string   `json:"id"`
	Author       string   `json:"author"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"lastModified"`
	PipelineTag  string   `json:"pipelineTag"`
	Tags         []string `json:"tags"`
	Readme       string   `json:"readme"` // markdown, YAML frontmatter stripped
}

// readmeMaxBytes caps how much of a model card is fetched and returned.
const readmeMaxBytes = 256 * 1024

// RepoDetail fetches repo metadata and its README.md model card. A missing
// README is not an error; Readme is just empty.
func (m *Manager) RepoDetail(ctx context.Context, token, repo string) (RepoDetail, error) {
	var info struct {
		ID           string   `json:"id"`
		Author       string   `json:"author"`
		Downloads    int64    `json:"downloads"`
		Likes        int64    `json:"likes"`
		LastModified string   `json:"lastModified"`
		PipelineTag  string   `json:"pipeline_tag"`
		Tags         []string `json:"tags"`
	}
	if err := m.hfGet(ctx, token, "/api/models/"+repo, nil, &info); err != nil {
		return RepoDetail{}, err
	}
	detail := RepoDetail{
		ID:           info.ID,
		Author:       info.Author,
		Downloads:    info.Downloads,
		Likes:        info.Likes,
		LastModified: info.LastModified,
		PipelineTag:  info.PipelineTag,
		Tags:         info.Tags,
	}
	if detail.Tags == nil {
		detail.Tags = []string{}
	}
	detail.Readme = m.fetchReadme(ctx, token, repo)
	return detail, nil
}

func (m *Manager) fetchReadme(ctx context.Context, token, repo string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/"+repo+"/raw/main/README.md", nil)
	if err != nil {
		return ""
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, readmeMaxBytes))
	if err != nil {
		return ""
	}
	return stripFrontmatter(string(data))
}

// stripFrontmatter removes a leading "---\n...\n---\n" YAML block (model card
// metadata) so only the human-readable markdown remains.
func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return s
	}
	rest := s[strings.Index(s, "\n")+1:]
	for _, end := range []string{"\n---\n", "\n---\r\n", "\r\n---\r\n"} {
		if idx := strings.Index(rest, end); idx >= 0 {
			return strings.TrimLeft(rest[idx+len(end):], "\r\n")
		}
	}
	return s
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
		// Only offer filenames safe for disk paths and the generated cmd;
		// see safeFileNameRe. Downloads accept only names from this listing.
		if !safeFileNameRe.MatchString(filepath.Base(s.Rfilename)) {
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
