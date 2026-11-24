package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/leandrodaf/barnraise/internal/app"
)

// The trust layer's side of the API (ADR-0029, ADR-0030): the guard that
// keeps a session token inside its session, and the plain HTTP endpoints of
// `barnraise events verify` and `barnraise trust`.

// sessionGuard checks every Connect call made with a session token against
// the session's grant, before the handler runs.
func (s *Server) sessionGuard() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			a := actorFrom(ctx)
			if a.Kind == app.ActorAgent {
				proc := path.Base(req.Spec().Procedure)
				var refs app.CallRefs
				if m, ok := req.Any().(proto.Message); ok {
					refs = callRefs(m)
				}
				if err := app.AuthorizeCall(a, proc, refs); err != nil {
					s.denied(ctx, a, proc, err)
					return nil, connectErr(err)
				}
			}
			return next(ctx, req)
		}
	}
}

// guardHTTP is sessionGuard for a plain HTTP route.
func (s *Server) guardHTTP(proc string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := actorFrom(r.Context())
		if err := app.AuthorizeCall(a, proc, app.CallRefs{Project: r.URL.Query().Get("project")}); err != nil {
			s.denied(r.Context(), a, proc, err)
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) denied(ctx context.Context, a app.Actor, proc string, err error) {
	s.svc.TokenDenied(context.WithoutCancel(ctx), a, proc, err)
	s.audit(ctx, "token.denied", proc, a.Project, err.Error())
}

// callRefs reads the project, the top-level id or target, and every
// item_id (at any depth) of a request.
func callRefs(m proto.Message) app.CallRefs {
	var refs app.CallRefs
	msg := m.ProtoReflect()
	fields := msg.Descriptor().Fields()
	str := func(name protoreflect.Name) string {
		if f := fields.ByName(name); f != nil && f.Kind() == protoreflect.StringKind && f.Cardinality() != protoreflect.Repeated {
			return msg.Get(f).String()
		}
		return ""
	}
	refs.Project = str("project")
	refs.ID = str("id")
	if t := str("target"); t != "" {
		refs.ID = t
	}
	walkItemIDs(msg, &refs.ItemIDs, 0)
	return refs
}

func walkItemIDs(msg protoreflect.Message, out *[]string, depth int) {
	if depth > 8 {
		return
	}
	msg.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case f.Kind() == protoreflect.StringKind && f.Name() == "item_id" && !f.IsList() && !f.IsMap():
			*out = append(*out, v.String())
		case f.Kind() == protoreflect.MessageKind && f.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				walkItemIDs(l.Get(i).Message(), out, depth+1)
			}
		case f.Kind() == protoreflect.MessageKind && !f.IsMap():
			walkItemIDs(v.Message(), out, depth+1)
		}
		return true
	})
}

func (s *Server) trustRoutes() {
	s.mux.Handle("GET /v1/events/verify", s.auth(http.HandlerFunc(s.handleVerify)))
	s.mux.Handle("GET /v1/trust", s.auth(http.HandlerFunc(s.handleTrust)))
	s.mux.Handle("POST /v1/trust", s.auth(http.HandlerFunc(s.handleTrust)))
}

// ownerOnly refuses session tokens and actors without admin.
func ownerOnly(w http.ResponseWriter, a app.Actor, project string) bool {
	if a.Kind == app.ActorAgent || !a.Can(app.ScopeAdmin, project) {
		writeErr(w, http.StatusForbidden, "admin required")
		return false
	}
	return true
}

// handleVerify walks the event chain: GET /v1/events/verify[?from=N].
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !ownerOnly(w, actorFrom(r.Context()), "") {
		return
	}
	if s.opts.Store == nil {
		writeErr(w, http.StatusServiceUnavailable, "no event store")
		return
	}
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	rep, err := s.opts.Store.VerifyChain(r.Context(), from)
	if err == nil {
		rep.Anchor, err = s.opts.Store.CheckAnchor(r.Context(), s.opts.VerifyAnchor)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, rep)
}

// handleTrust: GET reports what would refuse a session; POST re-trusts
// (?project=&item=).
func (s *Server) handleTrust(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r.Context())
	project, item := r.URL.Query().Get("project"), r.URL.Query().Get("item")
	if !ownerOnly(w, a, project) {
		return
	}
	var rep app.TrustReport
	var err error
	if r.Method == http.MethodPost {
		rep, err = s.svc.Trust(r.Context(), project, a, item)
		if err == nil {
			s.audit(r.Context(), "trust", item, rep.Project, strconv.Itoa(len(rep.Recorded))+" files")
		}
	} else {
		rep, err = s.svc.TrustStatus(r.Context(), project, a, item)
	}
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, app.ErrForbidden):
			code = http.StatusForbidden
		case errors.Is(err, app.ErrNotFound), errors.Is(err, app.ErrUnknownProj):
			code = http.StatusNotFound
		case errors.Is(err, app.ErrInvalid):
			code = http.StatusBadRequest
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, rep)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
