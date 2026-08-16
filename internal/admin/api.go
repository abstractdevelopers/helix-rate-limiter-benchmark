package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/config"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/limiter"
)

// API provides admin endpoints for managing rate limits.
type API struct {
	cfg    *config.Manager
	engine *limiter.Engine
}

// New creates a new admin API.
func New(cfg *config.Manager, engine *limiter.Engine) *API {
	return &API{cfg: cfg, engine: engine}
}

// Register routes the admin API on the given router.
func (a *API) Register(r chi.Router) {
	r.Get("/admin/limits", a.listLimits)
	r.Get("/admin/limits/{tier}", a.getLimit)
	r.Post("/admin/limits/{tier}", a.createLimit)
	r.Put("/admin/limits/{tier}", a.updateLimit)
	r.Delete("/admin/limits/{tier}", a.deleteLimit)
	r.Post("/admin/config/reload", a.reloadConfig)
	r.Get("/admin/health", a.health)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.Health(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis unreachable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) listLimits(w http.ResponseWriter, r *http.Request) {
	cfg := a.cfg.Get()
	limits := make(map[string]config.Limit)
	limits["default"] = cfg.DefaultLimit
	for k, v := range cfg.Clients {
		limits[k] = *v
	}
	writeJSON(w, http.StatusOK, limits)
}

func (a *API) getLimit(w http.ResponseWriter, r *http.Request) {
	tier := chi.URLParam(r, "tier")
	limit := a.cfg.GetLimit(tier)
	writeJSON(w, http.StatusOK, limit)
}

func (a *API) createLimit(w http.ResponseWriter, r *http.Request) {
	tier := chi.URLParam(r, "tier")
	var limit config.Limit
	if err := json.NewDecoder(r.Body).Decode(&limit); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := a.cfg.SetLimit(tier, &limit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, limit)
}

func (a *API) updateLimit(w http.ResponseWriter, r *http.Request) {
	tier := chi.URLParam(r, "tier")
	var limit config.Limit
	if err := json.NewDecoder(r.Body).Decode(&limit); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := a.cfg.SetLimit(tier, &limit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, limit)
}

func (a *API) deleteLimit(w http.ResponseWriter, r *http.Request) {
	tier := chi.URLParam(r, "tier")
	if err := a.cfg.RemoveLimit(tier); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "limit removed"})
}

func (a *API) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Reload(); err != nil {
		writeError(w, http.StatusBadRequest, "reload failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "config reloaded"})
}