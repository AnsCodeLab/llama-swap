package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/hub"
	"github.com/mostlygeek/llama-swap/internal/process"
	"github.com/mostlygeek/llama-swap/internal/router"
)

const hubSearchLimit = 30

// hubEnabled gates every /api/hub endpoint: both a manager and a configured
// modelsDir are required.
func (s *Server) hubEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.hub == nil || s.cfg.ModelsDir == "" {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"hub downloads are disabled: set modelsDir in the configuration file")
		return false
	}
	return true
}

func sendJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHubPopular(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	repos, err := s.hub.Search(r.Context(), s.cfg.HubToken, "", hubSearchLimit)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, repos)
}

func (s *Server) handleHubSearch(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "missing query parameter q")
		return
	}
	repos, err := s.hub.Search(r.Context(), s.cfg.HubToken, q, hubSearchLimit)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, repos)
}

func (s *Server) handleHubRepo(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	repo := r.PathValue("repo")
	if !hub.ValidRepoName(repo) {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid repo name, expected org/name")
		return
	}
	files, err := s.hub.ListFiles(r.Context(), s.cfg.HubToken, repo, s.cfg.ModelsDir)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if files == nil {
		files = []hub.RepoFile{}
	}
	sendJSON(w, files)
}

// handleHubDetail serves repo metadata plus its README model card for the
// UI's detail modal.
func (s *Server) handleHubDetail(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	repo := r.PathValue("repo")
	if !hub.ValidRepoName(repo) {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid repo name, expected org/name")
		return
	}
	detail, err := s.hub.RepoDetail(r.Context(), s.cfg.HubToken, repo)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	sendJSON(w, detail)
}

func (s *Server) handleHubDownloads(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "hub downloads are disabled")
		return
	}
	sendJSON(w, s.hub.Snapshot())
}

func (s *Server) handleHubDownload(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	var req struct {
		Repo string `json:"repo"`
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" || req.File == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with repo and file")
		return
	}
	if !hub.ValidRepoName(req.Repo) {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid repo name, expected org/name")
		return
	}

	files, err := s.hub.ListFiles(r.Context(), s.cfg.HubToken, req.Repo, s.cfg.ModelsDir)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	parts := hub.PartSet(files, req.File)
	if len(parts) == 0 {
		router.SendResponse(w, r, http.StatusNotFound, "file not found in repo")
		return
	}

	modelID := hub.DeriveModelID(req.File)
	if _, exists := s.cfg.Models[modelID]; exists {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", modelID, i)
			if _, exists := s.cfg.Models[candidate]; !exists {
				modelID = candidate
				break
			}
		}
	}

	mainPath := filepath.Join(s.cfg.ModelsDir, parts[0].Name)
	cmd := strings.ReplaceAll(s.cfg.HubCmdTemplate, "${MODEL_PATH}", mainPath)

	id, err := s.hub.StartDownload(hub.DownloadOpts{
		ModelsDir: s.cfg.ModelsDir,
		Token:     s.cfg.HubToken,
		Repo:      req.Repo,
		Files:     parts,
		ModelID:   modelID,
		Name:      strings.TrimSuffix(req.File, ".gguf"),
		Cmd:       cmd,
	})
	if err != nil {
		router.SendResponse(w, r, http.StatusConflict, err.Error())
		return
	}
	sendJSON(w, map[string]string{"id": id})
}

func (s *Server) handleHubDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "hub downloads are disabled")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with id")
		return
	}
	if !s.hub.Cancel(req.ID) {
		router.SendResponse(w, r, http.StatusNotFound, "no active download with that id")
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}

// handleHubDownloadsClear removes completed/cancelled/errored download jobs
// from the list so the UI can dismiss them. Active downloads are untouched.
func (s *Server) handleHubDownloadsClear(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "hub downloads are disabled")
		return
	}
	s.hub.ClearFinished()
	sendJSON(w, map[string]string{"msg": "ok"})
}

// handleHubDelete unloads a model, deletes its GGUF files (only those inside
// modelsDir), removes its config entry, and triggers a config reload.
func (s *Server) handleHubDelete(w http.ResponseWriter, r *http.Request) {
	if !s.hubEnabled(w, r) {
		return
	}
	var req struct {
		ModelID string `json:"modelId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ModelID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with modelId")
		return
	}

	realName, found := s.cfg.RealModelName(req.ModelID)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]

	if state, running := s.local.RunningModels()[realName]; running && state == process.StateStarting {
		router.SendResponse(w, r, http.StatusConflict, "model is starting, try again when it is ready")
		return
	}
	s.local.Unload(apiUnloadTimeout, realName)

	deleted, skipped := deleteModelFiles(mc.Cmd, s.cfg.ModelsDir)

	if _, err := s.hub.RemoveModel(realName); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("failed to update config: %v", err))
		return
	}

	sendJSON(w, map[string]any{"deleted": deleted, "skipped": skipped})
}

// handleHubHardware reports the machine's memory so the UI can rate model
// fit. Not gated on modelsDir: it is plain hardware info.
func (s *Server) handleHubHardware(w http.ResponseWriter, r *http.Request) {
	ramMB := 0
	if vm, err := mem.VirtualMemory(); err == nil {
		ramMB = int(vm.Total / (1024 * 1024))
	}
	vramMB := 0
	gpuName := ""
	if s.perf != nil {
		_, gpuStats := s.perf.Current()
		for _, g := range gpuStats {
			if g.MemTotalMB > vramMB {
				vramMB = g.MemTotalMB
				gpuName = g.Name
			}
		}
	}
	sendJSON(w, map[string]any{
		"ramMB":   ramMB,
		"vramMB":  vramMB,
		"gpuName": gpuName,
	})
}

// deleteModelFiles parses a macro-expanded cmd, collects -m/--model/--mmproj
// file arguments, and removes those inside modelsDir (plus stale .part
// files). Paths outside modelsDir are reported as skipped.
func deleteModelFiles(cmd, modelsDir string) (deleted, skipped []string) {
	deleted, skipped = []string{}, []string{}
	args, err := config.SanitizeCommand(cmd)
	if err != nil {
		return deleted, skipped
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-m", "--model", "--mmproj":
			path := args[i+1]
			abs, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(modelsDir, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				skipped = append(skipped, path)
				continue
			}
			if err := os.Remove(abs); err == nil {
				deleted = append(deleted, path)
			}
			os.Remove(abs + ".part")
		}
	}
	return deleted, skipped
}
