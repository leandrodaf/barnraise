package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/time/rate"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/adapters/out/github"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configure the server.
type Options struct {
	Version    string
	OwnerToken string // the owner's secret (file 0600 in the config dir)
	Store      *sqlite.Store
	Probe      func() domain.Machine
	Register   func(ctx context.Context, root string, paused bool) error
	Reload     func(ctx context.Context, project string) error
	// Peers lists the machines this daemon is paired with; StopPeers asks
	// them to stop their daemon (empty names: every connected one).
	Peers     func() []*pb.Peer
	StopPeers func(ctx context.Context, names []string) []*pb.PeerStop
	// SetStandby switches this machine's standby, keeps it in the machine's
	// configuration and passes it on to the named peers.
	SetStandby func(ctx context.Context, on bool, why string, peers []string) (*pb.StandbyResponse, error)
	// Capabilities and InstallTools answer for this machine and fan out to
	// the requested peers (ADR-0046); nil answers for this machine only.
	Capabilities func(ctx context.Context, r *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error)
	InstallTools func(ctx context.Context, a app.Actor, r *pb.InstallToolsRequest) (*pb.InstallToolsResponse, error)
	Panel        fs.FS  // static panel files
	OpenAPI      []byte // generated document
	// WebhookSecret returns the secret for inbound webhooks of a project and source (github, gitlab, inbound).
	WebhookSecret func(project, source string) string
	RatePerSec    float64
	Burst         int
	Log           *slog.Logger
	// VerifyAnchor checks the signature of the event chain's anchor (nil:
	// the anchor's position is checked, not its signature).
	VerifyAnchor func(domain.ChainAnchor) error
}

// Server is the HTTP side of the daemon.
type Server struct {
	svc      *app.Service
	opts     Options
	mux      *http.ServeMux
	limiters sync.Map // token id -> *rate.Limiter
}

// New builds the server.
func New(svc *app.Service, opts Options) *Server {
	if opts.RatePerSec <= 0 {
		opts.RatePerSec = 50
	}
	if opts.Burst <= 0 {
		opts.Burst = 100
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &Server{svc: svc, opts: opts, mux: http.NewServeMux()}
	path, handler := barnraisev1connect.NewBarnraiseServiceHandler(&Handler{srv: s}, connect.WithReadMaxBytes(8<<20), connect.WithCodec(snakeJSON{}),
		connect.WithInterceptors(s.sessionGuard()))
	s.mux.Handle(path, s.auth(handler))
	s.mux.Handle("GET /v1/events", s.auth(s.guardHTTP(app.ProcStreamEvents, http.HandlerFunc(s.handleSSE))))
	s.routeLessons()
	s.trustRoutes()
	s.mux.HandleFunc("POST /v1/webhooks/github", s.handleGitHub)
	s.mux.HandleFunc("POST /v1/webhooks/gitlab", s.handleGitLab)
	s.mux.HandleFunc("POST /v1/webhooks/inbound/{project}", s.handleInbound)
	s.mux.HandleFunc("GET /v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(opts.OpenAPI)
	})
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "machine": svc.Machine(), "version": opts.Version, "projects": svc.Projects()})
	})
	if opts.Panel != nil {
		s.mux.Handle("/", securityHeaders(http.FileServerFS(opts.Panel)))
	}
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.mux }

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		h.ServeHTTP(w, r)
	})
}

type actorKey struct{}

func actorFrom(ctx context.Context) app.Actor {
	if a, ok := ctx.Value(actorKey{}).(app.Actor); ok {
		return a
	}
	return app.Actor{Kind: app.ActorToken, Name: "anonymous"}
}

// WithActor is for tests and in-process callers.
func WithActor(ctx context.Context, a app.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// auth resolves the bearer token: owner, agent session, or scoped API token.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		a, ok := s.resolve(r.Context(), tok)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		key := a.TokenID
		if key == "" {
			key = a.Name
		}
		lim, _ := s.limiters.LoadOrStore(key, rate.NewLimiter(rate.Limit(s.opts.RatePerSec), s.opts.Burst))
		if !lim.(*rate.Limiter).Allow() {
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, "rate limit")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), a)))
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	if t := r.URL.Query().Get("token"); t != "" && r.URL.Path == "/v1/events" {
		return t // EventSource cannot set headers
	}
	return ""
}

func (s *Server) resolve(ctx context.Context, tok string) (app.Actor, bool) {
	if s.opts.OwnerToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.OwnerToken)) == 1 {
		return app.Owner(""), true
	}
	if a, ok := s.svc.Tokens().Resolve(tok); ok {
		return a, true
	}
	if s.opts.Store != nil {
		t, err := s.opts.Store.GetTokenByHash(ctx, app.HashToken(tok))
		if err == nil && t.Valid(time.Now()) {
			return app.Actor{Kind: app.ActorToken, Name: t.Name, Project: t.Project, Scopes: t.Scopes, TokenID: t.ID}, true
		}
	}
	return app.Actor{}, false
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) audit(ctx context.Context, action, target, project, detail string) {
	if s.opts.Store == nil {
		return
	}
	a := actorFrom(ctx)
	_ = s.opts.Store.AppendAudit(context.WithoutCancel(ctx), sqlite.AuditEntry{Actor: string(a.Kind) + ":" + a.Name, TokenID: a.TokenID, Action: action, Target: target, Project: project, Detail: detail})
}

func (s *Server) createToken(ctx context.Context, name, project string, scopes []string, ttl time.Duration) (*sqlite.Token, string, error) {
	if s.opts.Store == nil {
		return nil, "", errors.New("no token store")
	}
	secret := app.NewSecret("brt_")
	t := sqlite.Token{ID: "tok-" + strconv.FormatInt(time.Now().UnixNano(), 36), Name: name, Hash: app.HashToken(secret), Project: project, Scopes: scopes, Created: time.Now().UTC()}
	if ttl > 0 {
		t.Expires = time.Now().Add(ttl).UTC()
	}
	if err := s.opts.Store.CreateToken(ctx, t); err != nil {
		return nil, "", err
	}
	return &t, secret, nil
}

func (s *Server) listTokens(ctx context.Context) ([]*sqlite.Token, error) {
	if s.opts.Store == nil {
		return nil, nil
	}
	return s.opts.Store.ListTokens(ctx)
}

func (s *Server) revokeToken(ctx context.Context, id string) error {
	if s.opts.Store == nil {
		return errors.New("no token store")
	}
	err := s.opts.Store.RevokeToken(ctx, id)
	if errors.Is(err, sqlite.ErrNotFound) {
		return fmt.Errorf("%w: token %s", app.ErrNotFound, id)
	}
	return err
}

func (s *Server) listAudit(ctx context.Context, project string, limit int) ([]sqlite.AuditEntry, error) {
	if s.opts.Store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	return s.opts.Store.ListAudit(ctx, sqlite.AuditQuery{Project: project, Limit: limit})
}

func (s *Server) events(ctx context.Context, project, subject string, types []string, after int64, limit int) ([]domain.Event, error) {
	if s.opts.Store == nil {
		return nil, nil
	}
	evs, err := s.svc.Deps().Store.Load(ctx, project, after)
	if err != nil {
		return nil, err
	}
	var out []domain.Event
	for _, e := range evs {
		if subject != "" && e.Subject != subject {
			continue
		}
		if !app.MatchType(types, e.Type) {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// handleSSE streams CloudEvents: first the backlog after Last-Event-ID or
// ?after_seq, then live events.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r.Context())
	project := r.URL.Query().Get("project")
	if !a.Can(app.ScopeEvents, project) && !a.Can(app.ScopeItemsRead, project) {
		writeErr(w, http.StatusForbidden, "events:read required")
		return
	}
	if a.Project != "" {
		project = a.Project
	}
	var types []string
	if t := r.URL.Query().Get("types"); t != "" {
		types = strings.Split(t, ",")
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			after = n
		}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	live, cancel := s.svc.Hub().Subscribe(project, types, 512)
	defer cancel()
	sent := map[string]bool{}
	write := func(e domain.Event) bool {
		if sent[e.ID] {
			return true
		}
		sent[e.ID] = true
		if len(sent) > 10000 {
			sent = map[string]bool{e.ID: true}
		}
		ce := webhook.ToCloudEvent(e, webhook.Source(e.Machine, e.Project))
		data, _ := json.Marshal(ce)
		if e.Seq > 0 {
			fmt.Fprintf(w, "id: %d\n", e.Seq)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
		fl.Flush()
		return true
	}
	if after > 0 || r.URL.Query().Get("backlog") == "1" {
		evs, err := s.events(r.Context(), project, "", types, after, 1000)
		if err == nil {
			for _, e := range evs {
				write(e)
			}
		}
	}
	fmt.Fprintf(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				return
			}
			write(e)
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

var itemBranch = regexp.MustCompile(`^br/(I-\d+)`)

// handleGitHub receives push, check_run, workflow_run, issues and comments.
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	ev, err := github.ParseEvent(r.Header.Get("X-GitHub-Event"), body)
	if errors.Is(err, github.ErrUnsupportedEvent) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	project := s.projectForRepo(ev.Repo)
	if project == "" {
		writeErr(w, http.StatusNotFound, "no project for repository "+ev.Repo)
		return
	}
	secret := ""
	if s.opts.WebhookSecret != nil {
		secret = s.opts.WebhookSecret(project, "github")
	}
	if !github.VerifySignature([]byte(secret), body, r.Header.Get("X-Hub-Signature-256")) {
		writeErr(w, http.StatusUnauthorized, "bad signature")
		return
	}
	ctx := WithActor(r.Context(), app.Actor{Kind: app.ActorToken, Name: "github-webhook", Project: project, Scopes: []string{app.ScopeChecksReport, app.ScopeItemsWrite, app.ScopeItemsRead}})
	s.audit(ctx, "webhook.github."+ev.Type, ev.Branch, project, ev.Action)
	if err := s.applyGitHub(ctx, project, ev); err != nil {
		s.opts.Log.Warn("github webhook", "type", ev.Type, "err", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) projectForRepo(repo string) string {
	for _, name := range s.svc.Projects() {
		p, err := s.svc.Project(name)
		if err != nil {
			continue
		}
		if strings.EqualFold(p.Cfg().CodeHost.Repo, repo) || strings.EqualFold(p.Cfg().Tracker.Repo, repo) {
			return name
		}
	}
	return ""
}

func (s *Server) applyGitHub(ctx context.Context, project string, ev github.Event) error {
	p, err := s.svc.Project(project)
	if err != nil {
		return err
	}
	a := actorFrom(ctx)
	switch ev.Type {
	case "push":
		if ev.Branch == p.Cfg().MainBranch && len(ev.Files) > 0 {
			// Main moved outside the merge queue (a person merged): revalidate.
			_, err := s.svc.Revalidate(ctx, project, app.System(), domain.Change{Kind: "integration", Cause: "push to main " + ev.SHA, Commit: ev.SHA, Files: ev.Files})
			return err
		}
	case "check_run", "workflow_run":
		if ev.Status != "completed" {
			return nil
		}
		m := itemBranch.FindStringSubmatch(ev.Branch)
		if m == nil {
			return nil
		}
		for name, def := range p.Cfg().Checks {
			if def.RunsOn != "github-actions" {
				continue
			}
			wf := strings.TrimSuffix(strings.TrimSuffix(def.Workflow, ".yml"), ".yaml")
			if !strings.EqualFold(ev.Name, def.Workflow) && !strings.EqualFold(ev.Name, wf) && !strings.EqualFold(ev.Name, name) {
				continue
			}
			_, err := s.svc.ReportCheck(ctx, project, a, domain.CheckResult{ItemID: m[1], Check: name, Commit: ev.SHA, Passed: ev.Conclusion == "success",
				Summary: ev.Name + ": " + ev.Conclusion + " " + ev.URL, RunsOn: "github-actions"})
			return err
		}
	case "issues":
		if ev.Action == "opened" || ev.Action == "edited" || ev.Action == "labeled" {
			_, err := s.svc.ImportFromTracker(ctx, project, time.Now().Add(-10*time.Minute))
			return err
		}
	case "issue_comment":
		// "/barnraise approve plan I-42" from a trusted author.
		if !domain.Contains(p.Cfg().Tracker.Trusted, ev.Author) {
			return nil
		}
		f := strings.Fields(strings.TrimSpace(ev.Body))
		if len(f) >= 4 && f[0] == "/barnraise" && (f[1] == "approve" || f[1] == "reject") {
			return s.svc.Approve(ctx, project, app.Owner(ev.Author), f[3], f[2], f[1] == "approve", "from a GitHub comment by "+ev.Author)
		}
	}
	return nil
}

// handleGitLab receives pipeline and push hooks with a shared token.
func (s *Server) handleGitLab(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	var hook struct {
		ObjectKind string `json:"object_kind"`
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Project    struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
		ObjectAttributes struct {
			Ref    string `json:"ref"`
			SHA    string `json:"sha"`
			Status string `json:"status"`
			Name   string `json:"name"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &hook); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	project := s.projectForRepo(hook.Project.PathWithNamespace)
	if project == "" {
		writeErr(w, http.StatusNotFound, "no project for repository")
		return
	}
	secret := ""
	if s.opts.WebhookSecret != nil {
		secret = s.opts.WebhookSecret(project, "gitlab")
	}
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(r.Header.Get("X-Gitlab-Token"))) != 1 {
		writeErr(w, http.StatusUnauthorized, "bad token")
		return
	}
	ctx := WithActor(r.Context(), app.Actor{Kind: app.ActorToken, Name: "gitlab-webhook", Project: project, Scopes: []string{app.ScopeChecksReport}})
	if hook.ObjectKind == "pipeline" && (hook.ObjectAttributes.Status == "success" || hook.ObjectAttributes.Status == "failed") {
		if m := itemBranch.FindStringSubmatch(hook.ObjectAttributes.Ref); m != nil {
			p, _ := s.svc.Project(project)
			for name, def := range p.Cfg().Checks {
				if def.RunsOn == "github-actions" || def.RunsOn == "gitlab-ci" {
					_, _ = s.svc.ReportCheck(ctx, project, actorFrom(ctx), domain.CheckResult{ItemID: m[1], Check: name, Commit: hook.ObjectAttributes.SHA,
						Passed: hook.ObjectAttributes.Status == "success", Summary: "pipeline " + hook.ObjectAttributes.Status, RunsOn: def.RunsOn})
				}
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleInbound is the generic signed webhook: check results from any system
// (webhook checks, other CIs) and approvals from bots.
func (s *Server) handleInbound(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	secret := ""
	if s.opts.WebhookSecret != nil {
		secret = s.opts.WebhookSecret(project, "inbound")
	}
	if secret == "" {
		writeErr(w, http.StatusUnauthorized, "inbound webhooks are not configured for this project")
		return
	}
	if err := webhook.VerifyRequest(r, secret, body, 5*time.Minute); err != nil {
		writeErr(w, http.StatusUnauthorized, "bad signature")
		return
	}
	var msg struct {
		Type    string `json:"type"` // check.result
		Item    string `json:"item"`
		Check   string `json:"check"`
		Commit  string `json:"commit"`
		Passed  bool   `json:"passed"`
		Summary string `json:"summary"`
		LogURL  string `json:"log_url"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := WithActor(r.Context(), app.Actor{Kind: app.ActorToken, Name: "inbound-webhook", Project: project, Scopes: []string{app.ScopeChecksReport}})
	switch msg.Type {
	case "check.result", "":
		res := domain.CheckResult{ItemID: msg.Item, Check: msg.Check, Commit: msg.Commit, Passed: msg.Passed, Summary: msg.Summary, RunsOn: "webhook"}
		if msg.LogURL != "" {
			res.Artifacts = []string{msg.LogURL}
		}
		if _, err := s.svc.ReportCheck(ctx, project, actorFrom(ctx), res); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit(ctx, "webhook.check", msg.Item, project, msg.Check)
	default:
		writeErr(w, http.StatusBadRequest, "unknown type "+msg.Type)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// renderKnowledge turns a knowledge answer into short text for the CLI and MCP.
func renderKnowledge(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

// ListenUnix listens on a unix socket readable only by its owner.
func ListenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve runs the HTTP server on a listener until ctx ends.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	hs := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	err := hs.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeTLS runs with TLS (explicit exposure beyond localhost).
func (s *Server) ServeTLS(ctx context.Context, l net.Listener, cert, key string) error {
	hs := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	err := hs.ServeTLS(l, cert, key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

var _ = sort.Strings
var _ ports.Notification
