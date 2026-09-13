package web

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func Assets() (fs.FS, error) {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}

	return assets, nil
}
