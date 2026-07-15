package main

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/Noasamaa/soop-parser/internal/api"
	"github.com/Noasamaa/soop-parser/internal/config"
	"github.com/Noasamaa/soop-parser/web"
)

func main() {
	cfg := config.Load()

	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		log.Fatal(err)
	}

	srv := api.New(cfg, http.FS(sub))
	handler := api.LogRequest(srv.Handler())

	addr := cfg.Addr()
	log.Printf("live-parser (go) listening on http://%s public_base=%q auth=%v",
		addr, cfg.PublicBaseURL, cfg.AccessToken != "")
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}
