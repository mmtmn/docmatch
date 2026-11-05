// Command dm-web serves the site build inside the web image of deploy/local (Experimentar mode of
// the installer). It swaps the placeholder origins the site was built with for the real ones:
//
//	DM_WEB_ROOT          the build (default /srv/www)
//	DM_WEB_LISTEN        listen address (default 0.0.0.0:4321)
//	DM_PUBLIC_BASE_URL   the site origin the browser uses (http://localhost:<port>)
//	DM_API_BASE_URL      the API origin the browser uses (http://localhost:<port>)
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/webserve"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dm-web:", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run() error {
	site, err := webserve.Origin("DM_PUBLIC_BASE_URL", env("DM_PUBLIC_BASE_URL", "http://localhost:4321"))
	if err != nil {
		return err
	}
	api, err := webserve.Origin("DM_API_BASE_URL", env("DM_API_BASE_URL", "http://localhost:8080"))
	if err != nil {
		return err
	}
	root := env("DM_WEB_ROOT", "/srv/www")
	h, err := webserve.New(root, map[string]string{
		webserve.SitePlaceholder: site,
		webserve.APIPlaceholder:  api,
	})
	if err != nil {
		return err
	}
	addr := env("DM_WEB_LISTEN", "0.0.0.0:4321")
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "dm-web: %s em %s (site %s, API %s, %d arquivo(s) com origem trocada)\n", root, addr, site, api, h.Rendered())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
