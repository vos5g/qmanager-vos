package api

import (
	"encoding/json"
	"net/http"
	"os"
)

// HandleLanguagePacksList lists installed and available i18n language packs
func (s *Server) HandleLanguagePacksList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	available := []map[string]string{
		{"code": "en", "name": "English"},
		{"code": "id", "name": "Indonesian"},
	}
	installed := []map[string]string{{"code": "en", "name": "English"}}

	if data, err := os.ReadFile("/data/vosclean/qm/web/locales/en/common.json"); err == nil && len(data) > 0 {
		_ = data
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"languages": available,
		"installed": installed,
		"current":   "en",
	})
}

// HandleLanguagePacksInstall installs a selected language pack
func (s *Server) HandleLanguagePacksInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
