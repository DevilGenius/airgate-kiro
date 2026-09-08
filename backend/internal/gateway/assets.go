package gateway

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed all:webdist
var webDistFS embed.FS

// GetWebAssets serves only the assets compiled into this generation. Working
// directories and neighboring projects never influence a running artifact.
func (g *KiroGateway) GetWebAssets() map[string][]byte {
	assets := make(map[string][]byte)
	_ = fs.WalkDir(webDistFS, "webdist", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		content, err := webDistFS.ReadFile(path)
		if err != nil {
			return nil
		}
		relPath := strings.TrimPrefix(path, "webdist/")
		if relPath == "" || relPath == "placeholder.txt" {
			return nil
		}
		assets[relPath] = content
		return nil
	})
	return assets
}
