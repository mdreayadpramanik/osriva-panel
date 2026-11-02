package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// Agent binaries and install scripts are public: they contain no secrets, and
// servers fetch them before they have any credential.

func (s *Server) handleAgentDownloads(w http.ResponseWriter, r *http.Request) {
	builds := s.dist.Builds()
	type entry struct {
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
		URL    string `json:"url"`
	}
	out := make([]entry, len(builds))
	for i, b := range builds {
		out[i] = entry{b.OS, b.Arch, b.Size, b.SHA256, "/api/v1/agent/download/" + b.OS + "/" + b.Arch}
	}
	writeJSON(w, http.StatusOK, map[string]any{"builds": out})
}

func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	b, ok := s.dist.Get(chi.URLParam(r, "os"), chi.URLParam(r, "arch"))
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "agent_build_not_found", "this panel has no agent build for that platform", "")
		return
	}
	f, err := b.Open()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+b.FileName()+`"`)
	h.Set("Content-Length", strconv.FormatInt(b.Size, 10))
	h.Set("X-Checksum-SHA256", b.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) handleAgentDownloadSHA(w http.ResponseWriter, r *http.Request) {
	b, ok := s.dist.Get(chi.URLParam(r, "os"), chi.URLParam(r, "arch"))
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "agent_build_not_found", "this panel has no agent build for that platform", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.SHA256+"\n")
}

func (s *Server) handleAgentInstallSh(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write(s.dist.InstallSh())
}

func (s *Server) handleAgentInstallPs1(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(s.dist.InstallPs1())
}
