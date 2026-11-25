package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/leandrodaf/barnraise/internal/app"
)

// The panel switches one machine of the cluster at a time, leaving the one
// that serves it as it is: plain JSON next to the Connect API, like the
// documents. Admin only, never a session token (ADR-0029).
//
//	POST /v1/machines/{name}/standby   {"on": true, "why": "…"}
func (s *Server) routeMachines() {
	s.mux.Handle("POST /v1/machines/{name}/standby", s.auth(s.guardHTTP("SetMachineStandby", http.HandlerFunc(s.handleMachineStandby))))
}

type machineStandbyBody struct {
	On  bool   `json:"on"`
	Why string `json:"why,omitempty"`
}

func (s *Server) handleMachineStandby(w http.ResponseWriter, r *http.Request) {
	if err := actorFrom(r.Context()).Require(app.ScopeAdmin, ""); err != nil {
		writeAppErr(w, err)
		return
	}
	if s.opts.SetStandby == nil {
		writeErr(w, http.StatusNotImplemented, "this daemon has no standby")
		return
	}
	var b machineStandbyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "the body is {\"on\": true|false, \"why\": \"…\"}: "+err.Error())
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeAppErr(w, errors.New("name the machine"))
		return
	}
	// This machine is switched as itself; another one through this daemon.
	self := name == s.svc.Machine()
	var peers []string
	if !self {
		peers = []string{name}
	}
	out, err := s.opts.SetStandby(r.Context(), b.On, b.Why, peers, !self)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "machine.standby.off"
	if b.On {
		action = "machine.standby.on"
	}
	s.audit(r.Context(), action, name, "", b.Why)
	writeJSON(w, out)
}
