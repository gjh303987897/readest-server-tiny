package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

func Files() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
