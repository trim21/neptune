package web

import (
	_ "embed"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-playground/validator/v10"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/swgui"
	"github.com/swaggest/swgui/v5"

	"tyr/internal/core"
	"tyr/internal/util"
	"tyr/internal/version"
	"tyr/internal/web/jsonrpc"
	"tyr/internal/web/res"
)

//go:embed description.md
var desc string

const HeaderAuthorization = "Authorization"

func New(c *core.Client, token string, enableDebug bool) http.Handler {
	apiSchema := jsonrpc.OpenAPI{}
	apiSchema.Reflector().SpecEns().Info.
		WithTitle("JSON-RPC").
		WithVersion("0.0.1").
		WithDescription(desc)
	apiSchema.Reflector().SpecEns().
		SetAPIKeySecurity("api-key", HeaderAuthorization, openapi.InHeader, "need set api header")

	v := validator.New()

	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	h := &jsonrpc.Handler{
		OpenAPI:   &apiSchema,
		Validator: v,
	}

	r := chi.NewMux()
	r.Use(middleware.Recoverer)

	r.Handle("GET /metrics", promhttp.Handler())

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		res.Text(w, http.StatusOK, ".")
	})

	if enableDebug {
		info, ok := debug.ReadBuildInfo()
		if ok {
			r.Get("/debug/version", func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintln(w, version.Print())
				_, _ = fmt.Fprintln(w)
				_, _ = fmt.Fprintln(w, info.String())
			})
		} else {
			r.Get("/debug/version", func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintln(w, version.Print())
			})
		}
		r.Mount("/debug", middleware.Profiler())
	}

	AddTorrent(h, c)
	GetTorrent(h, c)
	MoveTorrent(h, c)

	var auth = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderAuthorization) != token {
				res.JSON(w, http.StatusUnauthorized, jsonrpc.Response{
					JSONRPC: "2.0",
					Error: &jsonrpc.Error{
						Code:    jsonrpc.CodeInvalidRequest,
						Message: "invalid token",
					},
				})

				return
			}

			next.ServeHTTP(w, r)

			return
		})
	}

	r.With(middleware.NoCache, auth).Handle("POST /json_rpc", h)

	r.Get("/docs/openapi.json", h.OpenAPI.ServeHTTP)

	r.Handle("GET /docs/*", v5.NewHandlerWithConfig(swgui.Config{
		Title:       apiSchema.Reflector().Spec.Info.Title,
		SwaggerJSON: "/docs/openapi.json",
		BasePath:    "/docs/",
		SettingsUI:  jsonrpc.SwguiSettings(util.StrMap{"layout": "'BaseLayout'"}, "/json_rpc"),
	}))

	r.Handle("GET /*", http.FileServerFS(frontendFS))

	return r
}
