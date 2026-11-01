package llm

import (
	"bytes"
	b64std "encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPrepareImage(t *testing.T) {
	small, err := PrepareImage(pngBytes(t, 40, 20))
	if err != nil || small.MediaType != "image/png" {
		t.Fatalf("small: %+v %v", small.MediaType, err)
	}
	big, err := PrepareImage(pngBytes(t, 4000, 1000))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(strings.NewReader(mustB64(t, big.Data)))
	if err != nil || cfg.Width != maxImageSide || cfg.Height != 392 {
		t.Fatalf("resize: %dx%d %v", cfg.Width, cfg.Height, err)
	}
	if _, err := PrepareImage([]byte("hello, not an image")); err == nil {
		t.Fatal("text accepted as image")
	}
}

func TestImageMarkersAndAnthropic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	os.WriteFile(p, pngBytes(t, 8, 8), 0o644)
	text, paths := ExtractImageMarkers("Image ok.\n" + ImageMarker(p))
	if text != "Image ok." || len(paths) != 1 || paths[0] != p {
		t.Fatalf("markers: %q %v", text, paths)
	}
	img, err := LoadImage(p)
	if err != nil {
		t.Fatal(err)
	}
	_, msgs := toAnthropic([]Message{
		{Role: "user", Content: "look", Images: []Image{img}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "view_image", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "c1", Content: "here", Images: []Image{img}},
	})
	if msgs[0].Content[0].Type != "image" || msgs[0].Content[0].Source.MediaType != "image/png" || msgs[0].Content[1].Text != "look" {
		t.Fatalf("user image: %+v", msgs[0].Content)
	}
	tr := msgs[2].Content[0]
	blocks, ok := tr.Content.([]anthBlock)
	if tr.Type != "tool_result" || !ok || len(blocks) != 2 || blocks[1].Type != "image" {
		t.Fatalf("tool_result image: %+v", tr)
	}
	// OpenAI shape: tool images move to a user turn with image_url parts.
	out := openAIMessages([]Message{{Role: "tool", ToolCallID: "c1", Content: "here", Images: []Image{img}}})
	if len(out) != 2 || out[1].Role != "user" || len(out[0].Images) != 0 {
		t.Fatalf("openai reshape: %+v", out)
	}
	b, _ := json.Marshal(out[1])
	if !strings.Contains(string(b), `"image_url"`) || !strings.Contains(string(b), "data:image/png;base64,") {
		t.Fatalf("openai json: %s", b)
	}
}

func TestCapImages(t *testing.T) {
	img := Image{MediaType: "image/png", Data: "x"}
	msgs := []Message{{Role: "user", Images: []Image{img, img, img}}, {Role: "tool", Images: []Image{img, img}}}
	msgs = CapImages(msgs, 3)
	if len(msgs[1].Images) != 2 || len(msgs[0].Images) != 1 || !strings.Contains(msgs[0].Content, "2 older image") {
		t.Fatalf("cap: %d %d %q", len(msgs[0].Images), len(msgs[1].Images), msgs[0].Content)
	}
}

func mustB64(t *testing.T, s string) string {
	b, err := base64Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func base64Decode(s string) ([]byte, error) { return b64std.StdEncoding.DecodeString(s) }
