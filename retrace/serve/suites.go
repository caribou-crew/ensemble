package serve

import (
	"net/http"

	"github.com/caribou-crew/ensemble/retrace/suites"
)

func (s *server) handleSuites(w http.ResponseWriter, r *http.Request) {
	WriteSuites(w, s.deps().Cwd)
}

// WriteSuites serves project-wide suite inventory, shared by both HTTP hosts.
// App-specific roots do not redefine the project's expected suite inventory.
func WriteSuites(w http.ResponseWriter, cwd string) {
	items, err := suites.Load(cwd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []suites.SuiteOverview{}
	}
	writeJSON(w, http.StatusOK, suites.Response{Suites: items})
}
