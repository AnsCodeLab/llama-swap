package hub

import (
	"net/http"
	"sync"

	"github.com/mostlygeek/llama-swap/internal/shared"
)

// Options configures a Manager.
type Options struct {
	BaseURL    string // HF base URL, default DefaultBaseURL (override in tests)
	ConfigPath string // path to config.yaml for entry add/remove
	Logger     Logger // optional
}

// Logger is the subset of logmon.Monitor the hub needs.
type Logger interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
}

// Manager owns HF API access and active download jobs. Create it once in
// main: it must survive config reloads so downloads keep running.
type Manager struct {
	baseURL    string
	configPath string
	httpClient *http.Client
	logger     Logger
	reloadFn   func()

	mu        sync.Mutex
	downloads map[string]*download
	nextSeq   int64
}

type download struct {
	info   shared.DownloadInfo
	cancel func()
	seq    int64 // insertion order, so the UI list doesn't reorder on every update
}

func NewManager(opts Options) *Manager {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	return &Manager{
		baseURL:    opts.BaseURL,
		configPath: opts.ConfigPath,
		httpClient: &http.Client{},
		logger:     opts.Logger,
		downloads:  make(map[string]*download),
	}
}

// SetReloadFunc registers the callback used to reload llama-swap's config
// after this manager edits config.yaml. Called asynchronously.
func (m *Manager) SetReloadFunc(fn func()) {
	m.reloadFn = fn
}

// RemoveModel deletes the model's config.yaml entry and requests a reload.
func (m *Manager) RemoveModel(modelID string) (bool, error) {
	found, err := RemoveModelEntry(m.configPath, modelID)
	if err != nil {
		return found, err
	}
	if found && m.reloadFn != nil {
		go m.reloadFn()
	}
	return found, nil
}
