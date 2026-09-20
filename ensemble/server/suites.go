package server

import (
	"net/http"

	"github.com/caribou-crew/ensemble/retrace/serve"
)

func (s *server) handleRetraceSuites(w http.ResponseWriter, r *http.Request) {
	cwd := "."
	if s.Cfg != nil && s.Cfg.Dir != "" {
		cwd = s.Cfg.Dir
	}
	serve.WriteSuites(w, cwd)
}
