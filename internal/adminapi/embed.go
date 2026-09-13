package adminapi

import (
	"embed"
	"io/fs"
)

//go:embed web/dist/*
var webAssets embed.FS

func embeddedAssets() fs.FS {
	v, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		panic(err)
	}
	return v
}
