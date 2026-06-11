package shared

const ProcessStateChangeEventID = 0x01
const ConfigFileChangedEventID = 0x03
const ActivityLogEventID = 0x05
const ModelPreloadedEventID = 0x06
const InFlightRequestsEventID = 0x07

// ProcessStateChangeEvent is emitted whenever a process transitions between
// lifecycle states. States are carried as strings so this package stays a leaf
// (no import of internal/process).
type ProcessStateChangeEvent struct {
	ProcessName string
	OldState    string
	NewState    string
}

func (e ProcessStateChangeEvent) Type() uint32 {
	return ProcessStateChangeEventID
}

type ReloadingState int

const (
	ReloadingStateStart ReloadingState = iota
	ReloadingStateEnd
)

type ConfigFileChangedEvent struct {
	State ReloadingState
}

func (e ConfigFileChangedEvent) Type() uint32 {
	return ConfigFileChangedEventID
}

type ModelPreloadedEvent struct {
	ModelName string
	Success   bool
}

func (e ModelPreloadedEvent) Type() uint32 {
	return ModelPreloadedEventID
}

type InFlightRequestsEvent struct {
	Total int
}

func (e InFlightRequestsEvent) Type() uint32 {
	return InFlightRequestsEventID
}

const DownloadStatusEventID = 0x08

// DownloadInfo is the status of one hub download job (may span multiple
// files for multi-part GGUFs). JSON tags match the UI's DownloadInfo type.
type DownloadInfo struct {
	ID              string  `json:"id"`
	Repo            string  `json:"repo"`
	File            string  `json:"file"`
	ModelID         string  `json:"modelId"`
	State           string  `json:"state"` // downloading | completed | error | cancelled
	TotalBytes      int64   `json:"totalBytes"`
	DownloadedBytes int64   `json:"downloadedBytes"`
	SpeedBps        float64 `json:"speedBps"`
	Error           string  `json:"error,omitempty"`
}

// DownloadStatusEvent carries a full snapshot of all download jobs.
type DownloadStatusEvent struct {
	Downloads []DownloadInfo
}

func (e DownloadStatusEvent) Type() uint32 {
	return DownloadStatusEventID
}
