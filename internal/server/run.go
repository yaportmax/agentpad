package server

import (
	"io/fs"
	"net/http"

	"github.com/cyrusaf/agentpad/internal/config"
	"github.com/cyrusaf/agentpad/internal/store"
	"github.com/cyrusaf/agentpad/internal/webassets"
)

func Run(cfg config.Config) error {
	st, err := store.Open(cfg.Storage.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	staticFS, err := fs.Sub(webassets.FS, "dist")
	if err != nil {
		return err
	}

	app := NewWithStaticFS(st, staticFS)
	return http.ListenAndServe(cfg.Server.Address, app.Routes())
}
