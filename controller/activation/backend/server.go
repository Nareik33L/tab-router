package backend

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Server is the activation HTTP API.
type Server struct {
	Store *Store
	Proxy ProxyAccess
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/activate", s.activate)
	mux.HandleFunc("/v1/validate", s.validate)
	mux.HandleFunc("/v1/deactivate", s.deactivate)
	return mux
}

type activateRequest struct {
	Key            string `json:"key"`
	InstallationID string `json:"installation_id"`
}

type tokenRequest struct {
	InstallationID string `json:"installation_id"`
	Token          string `json:"token"`
}

type activateResponse struct {
	InstallationID string    `json:"installation_id"`
	Token          string    `json:"token"`
	Status         string    `json:"status"`
	ExpiresAt      string    `json:"expires_at"`
	Proxy          proxyJSON `json:"proxy"`
}

type proxyJSON struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	Country        string `json:"country,omitempty"`
	SessionMinutes int    `json:"session_minutes,omitempty"`
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req activateRequest
	if err := decode(r, &req); err != nil {
		writeCode(w, http.StatusBadRequest, "invalid_request")
		return
	}
	res, err := s.Store.Activate(req.Key, req.InstallationID, s.Proxy)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeResult(w, res, s.Proxy)
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req tokenRequest
	if err := decode(r, &req); err != nil {
		writeCode(w, http.StatusBadRequest, "invalid_request")
		return
	}
	res, err := s.Store.Validate(req.InstallationID, req.Token, s.Proxy)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeResult(w, res, s.Proxy)
}

func (s *Server) deactivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req tokenRequest
	if err := decode(r, &req); err != nil {
		writeCode(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := s.Store.Deactivate(req.InstallationID, req.Token); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": StatusRevoked})
}

func writeResult(w http.ResponseWriter, res Result, proxy ProxyAccess) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(activateResponse{
		InstallationID: res.InstallationID,
		Token:          res.Token,
		Status:         res.Status,
		ExpiresAt:      res.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		Proxy: proxyJSON{
			Host:           proxy.Host,
			Port:           proxy.Port,
			Username:       res.ProxyUser,
			Password:       res.ProxyPassword,
			Country:        proxy.Country,
			SessionMinutes: proxy.SessionMinutes,
		},
	})
}

func writeErr(w http.ResponseWriter, err error) {
	code := "unavailable"
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, ErrInvalidKey), errors.Is(err, ErrInvalidTok):
		code, status = err.Error(), http.StatusUnauthorized
	case errors.Is(err, ErrExpired), errors.Is(err, ErrRevoked), errors.Is(err, ErrLimit):
		code, status = err.Error(), http.StatusForbidden
	case errors.Is(err, ErrNoProxy):
		code, status = err.Error(), http.StatusServiceUnavailable
	}
	writeCode(w, status, code)
}

func writeCode(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func decode(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	return dec.Decode(dest)
}
