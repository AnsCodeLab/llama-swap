package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/router"
)

// settingsEnabled gates every /api/settings write endpoint: persisting
// changes requires knowing where config.yaml lives, and (to prevent an
// anonymous visitor from planting their own credential on a freshly
// installed, unconfigured server) requires that at least one credential
// already exists.
func (s *Server) settingsEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.configPath == "" {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"settings management is unavailable: no config file path")
		return false
	}
	if !s.hasBootstrapCredential() {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"settings management is locked: add one apiKeys entry or an auth.username/password pair to config.yaml by hand first (see docs/configuration.md), then this page can manage further keys and credentials")
		return false
	}
	return true
}

// hasBootstrapCredential reports whether the loaded config already has at
// least one API key or auth credential configured. When neither is set (the
// server's default, out-of-the-box state), the global auth middleware
// (CreateGlobalAuthMiddleware) is a full pass-through, meaning any anonymous
// visitor who can reach the server over the network could otherwise call the
// mutating settings endpoints to plant a persistent API key or set their own
// auth credentials, locking out the real operator. Requiring an existing
// credential first closes that path: once any credential exists, the global
// auth middleware already requires it for every route, including these.
func (s *Server) hasBootstrapCredential() bool {
	return len(s.cfg.RequiredAPIKeys) > 0 || s.cfg.Auth.Username != ""
}

func maskAPIKey(key string) string {
	if len(key) <= 10 {
		return "••••••"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

func (s *Server) handleSettingsAuthGet(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, map[string]any{
		"enabled":  s.cfg.Auth.Username != "" || s.cfg.Auth.Password != "",
		"username": s.cfg.Auth.Username,
	})
}

func (s *Server) handleSettingsAuthSet(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with username and password")
		return
	}
	if (req.Username == "") != (req.Password == "") {
		router.SendResponse(w, r, http.StatusBadRequest, "username and password must both be set, or both empty to disable protection")
		return
	}
	if err := config.SetAuthCredentials(s.configPath, req.Username, req.Password); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to save credentials: %v", err))
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}

func (s *Server) handleSettingsAPIKeysList(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		CreatedAt string `json:"createdAt"`
		MaskedKey string `json:"maskedKey"`
	}
	out := make([]entry, 0, len(s.cfg.RequiredAPIKeys))
	for _, k := range s.cfg.RequiredAPIKeys {
		out = append(out, entry{ID: k.ID, Label: k.Label, CreatedAt: k.CreatedAt, MaskedKey: maskAPIKey(k.Key)})
	}
	sendJSON(w, out)
}

func (s *Server) handleSettingsAPIKeyGenerate(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			router.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	label := strings.TrimSpace(req.Label)

	id, key, err := config.AddAPIKey(s.configPath, label)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to save API key: %v", err))
		return
	}
	sendJSON(w, map[string]string{"id": id, "key": key, "label": label})
}

func (s *Server) handleSettingsAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.settingsEnabled(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "expected JSON body with id")
		return
	}
	found, err := config.RemoveAPIKey(s.configPath, req.ID)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to update config: %v", err))
		return
	}
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "no API key with that id")
		return
	}
	sendJSON(w, map[string]string{"msg": "ok"})
}
