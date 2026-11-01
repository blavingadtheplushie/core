package llm

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"regexp"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder registration
)

// Image is a picture sent to a vision model alongside a message.
type Image struct {
	MediaType string // image/png, image/jpeg, image/gif, image/webp
	Data      string // base64, no data: prefix
	Name      string // for logs/labels only
}

func (i Image) DataURL() string { return "data:" + i.MediaType + ";base64," + i.Data }

// Vision limits: Anthropic recommends ≤1568 px on the long side and caps a
// base64 image at 5 MB; we stay well under both.
const (
	maxImageSide  = 1568
	maxImageBytes = 3_500_000
	// ImageTokens is the rough prompt cost of one image (for compaction).
	ImageTokens = 1600
)

// IsImageType tells whether a MIME type can be sent to a vision model.
func IsImageType(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}

// LoadImage reads an image file and returns it ready for a vision model.
func LoadImage(path string) (Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	img, err := PrepareImage(data)
	if err != nil {
		return Image{}, fmt.Errorf("%s: %w", path, err)
	}
	img.Name = path
	return img, nil
}

// PrepareImage validates raw bytes, and downsizes / re-encodes them when
// they are too big or in a format the APIs don't take as is.
func PrepareImage(data []byte) (Image, error) {
	if len(data) == 0 {
		return Image{}, errors.New("empty image")
	}
	mime := http.DetectContentType(data)
	if !IsImageType(mime) {
		return Image{}, fmt.Errorf("not a supported image (%s): png, jpeg, gif or webp only", mime)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("unreadable image: %w", err)
	}
	if cfg.Width*cfg.Height > 80_000_000 {
		return Image{}, fmt.Errorf("image too large (%dx%d)", cfg.Width, cfg.Height)
	}
	if len(data) <= maxImageBytes && cfg.Width <= maxImageSide && cfg.Height <= maxImageSide {
		return Image{MediaType: mime, Data: base64.StdEncoding.EncodeToString(data)}, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("unreadable image: %w", err)
	}
	w, h := cfg.Width, cfg.Height
	if w > maxImageSide || h > maxImageSide {
		if w >= h {
			h, w = max(1, h*maxImageSide/w), maxImageSide
		} else {
			w, h = max(1, w*maxImageSide/h), maxImageSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	outMime := "image/jpeg"
	if mime == "image/png" || mime == "image/gif" {
		// Keep screenshots/diagrams crisp; fall back to JPEG if still huge.
		png.Encode(&buf, dst)
		outMime = "image/png"
	}
	if outMime == "image/jpeg" || buf.Len() > maxImageBytes {
		buf.Reset()
		outMime = "image/jpeg"
		for q := 88; q >= 40; q -= 12 {
			buf.Reset()
			jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q})
			if buf.Len() <= maxImageBytes {
				break
			}
		}
	}
	return Image{MediaType: outMime, Data: base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

var _ = gif.Decode // gif decoder registration

// imageMarker lets a tool attach a picture to its result: the runner turns
// `[[image:/abs/path.png]]` into an image the model can see.
var imageMarker = regexp.MustCompile(`\[\[image:([^\]\n]+)\]\]`)

// ImageMarker builds the marker a tool prints to attach an image.
func ImageMarker(path string) string { return "[[image:" + path + "]]" }

// ExtractImageMarkers returns the text without markers and the paths found.
func ExtractImageMarkers(s string) (string, []string) {
	var paths []string
	for _, m := range imageMarker.FindAllStringSubmatch(s, 8) {
		paths = append(paths, strings.TrimSpace(m[1]))
	}
	if len(paths) == 0 {
		return s, nil
	}
	return strings.TrimSpace(imageMarker.ReplaceAllString(s, "")), paths
}

// CapImages keeps only the `keep` most recent images in a conversation (older
// ones are replaced by a short note) so prompts stay small.
func CapImages(msgs []Message, keep int) []Message {
	n := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(msgs[i].Images) == 0 {
			continue
		}
		if n+len(msgs[i].Images) <= keep {
			n += len(msgs[i].Images)
			continue
		}
		m := msgs[i]
		allowed := max(0, keep-n)
		dropped := len(m.Images) - allowed
		m.Images = m.Images[len(m.Images)-allowed:]
		n += allowed
		m.Content = strings.TrimSpace(m.Content + fmt.Sprintf("\n[%d older image(s) removed from context; use view_image again if needed]", dropped))
		msgs[i] = m
	}
	return msgs
}
