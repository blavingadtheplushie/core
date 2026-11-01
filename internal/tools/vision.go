package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/store"
)

func registerVision(d *Deps) {
	d.Reg.Add(&core.Tool{
		Name: "view_image", Category: "web", Emoji: "👀",
		Description: "Looks at an image (vision): a file in the workspace or /tmp (e.g. a Slack attachment in inbox/, a screenshot you made) or a public http(s) image URL. The picture is shown to you in the tool result so you can describe it, read text in it, check a render, etc. PNG, JPEG, GIF, WebP; big images are downsized.",
		Params: core.Obj(map[string]any{
			"path": core.Str("Image file path, or an http(s) URL."),
		}, "path"),
		Summarize: summarizeField("Looking at ", "path", 70),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ Path string }](args)
			src := strings.TrimSpace(a.Path)
			if src == "" {
				return "", errors.New("path is required")
			}
			var p string
			if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
				var err error
				if p, err = downloadImage(ctx, src); err != nil {
					return "", err
				}
			} else {
				var err error
				if p, err = d.resolvePath(src); err != nil {
					return "", err
				}
			}
			img, err := llm.LoadImage(p)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Image %s (%s, ~%d KB sent).\n%s", src, img.MediaType, len(img.Data)*3/4/1024, llm.ImageMarker(p)), nil
		},
	})
}

func downloadImage(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Blavingad/1.0)")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 25<<20 {
		return "", errors.New("image larger than 25 MB")
	}
	if ct := http.DetectContentType(data); !llm.IsImageType(ct) {
		return "", fmt.Errorf("the URL is not an image (%s)", ct)
	}
	dir := filepath.Join(os.TempDir(), "blavingad-vision")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, store.NewID("img"))
	return p, os.WriteFile(p, data, 0o644)
}
