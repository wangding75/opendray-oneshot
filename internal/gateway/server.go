// Package gateway is opendray's HTTP layer: chi router + middleware +
// route mounting. It owns no business state — every handler delegates to a
// subsystem passed in via Deps.
//
// Subsystem boundaries (design §6 ordering):
//   - HTTP router is dumb — it routes requests to subsystems.
//   - Subsystems own their state and communicate via the event bus.
//   - Auth is enforced as middleware at the router level *and* defended in
//     depth by each subsystem.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/opendray/opendray-v2/internal/version"
	"github.com/opendray/opendray-v2/internal/web"
)

// Pinger is satisfied by *store.Store. Defined here so gateway/ does not
// import store/ — accept interfaces, return structs (design §15).
type Pinger interface {
	Ping(ctx context.Context) error
}

type Deps struct {
	Logger    *slog.Logger
	DB        Pinger
	Version   version.Info
	StartedAt time.Time

	// V1Routes is called with the /api/v1 subrouter so subsystems can
	// mount themselves without making the gateway package depend on
	// them. The composition root in internal/app provides this hook.
	V1Routes func(chi.Router)
}

// Server is opendray's HTTP server with mounted v1 routes.
type Server struct {
	deps Deps
	mux  http.Handler
}

func NewServer(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.StartedAt.IsZero() {
		deps.StartedAt = time.Now()
	}
	s := &Server{deps: deps}
	s.mux = s.routes()
	return s
}

// Handler returns the http.Handler ready to mount on a net/http server.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogMiddleware(s.deps.Logger))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		if s.deps.V1Routes != nil {
			s.deps.V1Routes(r)
		}
	})

	// Embedded admin SPA. /admin/* served from internal/web's embed.FS;
	// the SPA's client-side router (TanStack) handles deep links via
	// the SPA fallback inside web.Handler. http.StripPrefix removes
	// the /admin prefix so web.Handler sees relative paths regardless
	// of chi's internal mount behaviour.
	r.Mount("/admin", http.StripPrefix("/admin", web.Handler()))

	// Convenience: bare GET / redirects to /admin/.
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	return r
}
