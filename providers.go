package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	env "github.com/2HgO/quidax-go/config"
	"github.com/2HgO/quidax-go/handlers"
	"github.com/MadAppGang/httplog"
	lzap "github.com/MadAppGang/httplog/zap"
	gHandlers "github.com/gorilla/handlers"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

func NewHttpServer(lc fx.Lifecycle, mux *http.ServeMux, log *zap.Logger) *http.Server {
	config := httplog.LoggerConfig{
		Formatter: lzap.ZapLogger(log, zap.InfoLevel, "quidax-go"),
	}
	opts := []gHandlers.CORSOption{
		gHandlers.AllowCredentials(),
		gHandlers.AllowedHeaders([]string{"keep-alive", "user-agent", "cache-control", "authorization", "content-type", "content-transfer-encoding", "x-accept-content-transfer-encoding", "x-accept-response-streaming", "x-user-agent", "referer", "x-trace-id", "origin", "x-requested-with"}),
		gHandlers.AllowedMethods([]string{"GET", "PUT", "DELETE", "POST", "PATCH", "OPTIONS"}),
		gHandlers.AllowedOrigins([]string{"*"}),
		gHandlers.ExposedHeaders([]string{"x-envoy-upstream-service-time", "x-total-count", "x-page-number", "x-per-page"}),
		gHandlers.MaxAge(1728000),
	}
	srv := &http.Server{
		// Addr: ":55059",
		// Addr: "0.0.0.0:8080",
		Addr: env.PORT,
		// todo: handler request logger manually
		Handler:      gHandlers.CORS(opts...)(httplog.LoggerWithConfig(config)(handlers.RecoveryMW(mux))),
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
			fmt.Println("Starting HTTP server at", srv.Addr)
			go srv.Serve(ln)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	})

	return srv
}

func NewServeMux(routers []handlers.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	for _, router := range routers {
		router.ServeHttp(mux)
	}
	return mux
}
