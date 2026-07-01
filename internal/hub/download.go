package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/shared"
)

const progressInterval = 500 * time.Millisecond

// DownloadOpts describes one download job. Files must be the full part set
// (one entry for single-file models). Cmd is the already-rendered cmd for the
// auto-added config entry (with ${MODEL_PATH} substituted by the caller).
type DownloadOpts struct {
	ModelsDir string
	Token     string
	Repo      string
	Files     []RepoFile
	ModelID   string
	Name      string
	Cmd       string
}

// StartDownload begins a download job in the background and returns its ID
// (the model ID). It fails fast on duplicate active jobs or insufficient
// disk space.
func (m *Manager) StartDownload(opts DownloadOpts) (string, error) {
	if len(opts.Files) == 0 {
		return "", fmt.Errorf("no files to download")
	}
	var total int64
	for _, f := range opts.Files {
		total += f.Size
	}
	if usage, err := disk.Usage(opts.ModelsDir); err == nil && usage.Free < uint64(total) {
		return "", fmt.Errorf("not enough disk space: need %d bytes, %d free", total, usage.Free)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if d, exists := m.downloads[opts.ModelID]; exists && d.info.State == "downloading" {
		return "", fmt.Errorf("download already in progress for %s", opts.ModelID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := &download{
		info: shared.DownloadInfo{
			ID:         opts.ModelID,
			Repo:       opts.Repo,
			File:       opts.Files[0].Name,
			ModelID:    opts.ModelID,
			State:      "downloading",
			TotalBytes: total,
		},
		cancel: cancel,
		seq:    m.nextSeq,
	}
	m.nextSeq++
	m.downloads[opts.ModelID] = d
	go m.runDownload(ctx, d, opts)
	m.emitLocked()
	return opts.ModelID, nil
}

// Cancel stops an active download. The .part file is kept for resume.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok || d.info.State != "downloading" {
		return false
	}
	d.cancel()
	return true
}

// ClearFinished removes every download job that is no longer active
// (completed, cancelled, or errored) so the UI list can be dismissed.
// Jobs still downloading are left untouched.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, d := range m.downloads {
		if d.info.State != "downloading" {
			delete(m.downloads, id)
		}
	}
	m.emitLocked()
}

// Snapshot returns the current state of every download job, ordered by when
// each job started.
func (m *Manager) Snapshot() []shared.DownloadInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// snapshotLocked builds the download list sorted by insertion order; callers
// must hold m.mu. Go map iteration order is randomized, so without this sort
// the list would visibly reorder on every progress tick.
func (m *Manager) snapshotLocked() []shared.DownloadInfo {
	list := make([]*download, 0, len(m.downloads))
	for _, d := range m.downloads {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].seq < list[j].seq })
	out := make([]shared.DownloadInfo, len(list))
	for i, d := range list {
		out[i] = d.info
	}
	return out
}

// emitLocked publishes a full snapshot; callers must hold m.mu.
func (m *Manager) emitLocked() {
	event.Emit(shared.DownloadStatusEvent{Downloads: m.snapshotLocked()})
}

func (m *Manager) setState(d *download, state, errMsg string) {
	m.mu.Lock()
	d.info.State = state
	d.info.Error = errMsg
	d.info.SpeedBps = 0
	m.emitLocked()
	m.mu.Unlock()
}

func (m *Manager) runDownload(ctx context.Context, d *download, opts DownloadOpts) {
	var doneBytes int64
	for _, f := range opts.Files {
		n, err := m.downloadFile(ctx, d, opts, f, doneBytes)
		if err != nil {
			if ctx.Err() != nil {
				m.setState(d, "cancelled", "")
			} else {
				if m.logger != nil {
					m.logger.Warnf("hub: download %s failed: %v", d.info.ID, err)
				}
				m.setState(d, "error", err.Error())
			}
			return
		}
		doneBytes += n
	}

	if err := AddModelEntry(m.configPath, opts.ModelID, opts.Name, opts.Cmd); err != nil {
		m.setState(d, "error", fmt.Sprintf("downloaded, but failed to update config: %v", err))
		return
	}
	m.setState(d, "completed", "")
	if m.logger != nil {
		m.logger.Infof("hub: download %s completed, added model entry", d.info.ID)
	}
	if m.reloadFn != nil {
		go m.reloadFn()
	}
}

// downloadFile fetches one file to modelsDir with .part resume. Returns the
// file's full size on success. prevBytes is the byte count of already-completed
// files in this job, used for progress reporting.
func (m *Manager) downloadFile(ctx context.Context, d *download, opts DownloadOpts, f RepoFile, prevBytes int64) (int64, error) {
	dest := filepath.Join(opts.ModelsDir, f.Name)
	if st, err := os.Stat(dest); err == nil {
		return st.Size(), nil // already downloaded
	}

	part := dest + ".part"
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	u := fmt.Sprintf("%s/%s/resolve/main/%s", m.baseURL, opts.Repo, url.PathEscape(f.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		offset = 0 // server ignored Range; start over
		flags |= os.O_TRUNC
	default:
		return 0, fmt.Errorf("huggingface returned %s for %s", resp.Status, f.Name)
	}

	out, err := os.OpenFile(part, flags, 0644)
	if err != nil {
		return 0, err
	}

	written := offset
	lastEmit := time.Now()
	lastBytes := written
	buf := make([]byte, 256*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return 0, werr
			}
			written += int64(n)
			if now := time.Now(); now.Sub(lastEmit) >= progressInterval {
				secs := now.Sub(lastEmit).Seconds()
				m.mu.Lock()
				d.info.DownloadedBytes = prevBytes + written
				d.info.SpeedBps = float64(written-lastBytes) / secs
				m.emitLocked()
				m.mu.Unlock()
				lastEmit = now
				lastBytes = written
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return 0, readErr
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(part, dest); err != nil {
		return 0, err
	}

	m.mu.Lock()
	d.info.DownloadedBytes = prevBytes + written
	m.emitLocked()
	m.mu.Unlock()
	return written, nil
}
