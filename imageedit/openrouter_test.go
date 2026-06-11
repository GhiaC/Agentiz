package imageedit

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterEditor_EditImage_Success(t *testing.T) {
	wantImage := []byte("EDITED-IMAGE-BYTES")
	var gotReq map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)

		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(wantImage)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{
					"content": "",
					"images": []map[string]any{
						{"type": "image_url", "image_url": map[string]any{"url": dataURL}},
					},
				}},
			},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 34, "total_tokens": 46},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ed := NewOpenRouter(OpenRouterConfig{APIKey: "test-key", BaseURL: srv.URL})
	res, err := ed.EditImage([]byte{0x89, 0x50}, "image/png", "add a hat")
	if err != nil {
		t.Fatalf("EditImage: %v", err)
	}
	if string(res.Data) != string(wantImage) {
		t.Errorf("output bytes mismatch: got %q", res.Data)
	}
	if res.MIMEType != "image/png" {
		t.Errorf("mime: got %q want image/png", res.MIMEType)
	}
	if res.Model != DefaultModel {
		t.Errorf("model: got %q want %q", res.Model, DefaultModel)
	}
	if res.InputTokens != 12 || res.OutputTokens != 34 {
		t.Errorf("usage not parsed: in=%d out=%d", res.InputTokens, res.OutputTokens)
	}

	// Request must carry modalities and a [text, image_url(data URL)] content.
	if mods, _ := gotReq["modalities"].([]any); len(mods) == 0 || mods[0] != "image" {
		t.Errorf("modalities not sent correctly: %v", gotReq["modalities"])
	}
	msgs, _ := gotReq["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	var foundText, foundImage bool
	for _, c := range content {
		cm, _ := c.(map[string]any)
		if cm["type"] == "text" && cm["text"] == "add a hat" {
			foundText = true
		}
		if cm["type"] == "image_url" {
			iu, _ := cm["image_url"].(map[string]any)
			if url, _ := iu["url"].(string); strings.HasPrefix(url, "data:image/png;base64,") {
				foundImage = true
			}
		}
	}
	if !foundText || !foundImage {
		t.Errorf("request content missing parts: text=%v image=%v", foundText, foundImage)
	}
}

func TestOpenRouterEditor_NoImageReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{"choices": []map[string]any{
			{"message": map[string]any{"content": "I cannot edit this", "images": []any{}}},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ed := NewOpenRouter(OpenRouterConfig{APIKey: "k", BaseURL: srv.URL})
	if _, err := ed.EditImage([]byte{1, 2, 3}, "image/png", "x"); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("expected a no-image error, got %v", err)
	}
}

func TestOpenRouterEditor_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	ed := NewOpenRouter(OpenRouterConfig{APIKey: "k", BaseURL: srv.URL})
	if _, err := ed.EditImage([]byte{1}, "image/png", "x"); err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
}

func TestOpenRouterEditor_MissingAPIKey(t *testing.T) {
	ed := NewOpenRouter(OpenRouterConfig{}) // no key
	if _, err := ed.EditImage([]byte{1}, "image/png", "x"); err == nil {
		t.Fatal("expected an error when API key is missing")
	}
}

func TestDecodeDataURL(t *testing.T) {
	u := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("hello"))
	b, mime, err := decodeDataURL(u)
	if err != nil {
		t.Fatalf("decodeDataURL: %v", err)
	}
	if string(b) != "hello" || mime != "image/jpeg" {
		t.Errorf("got %q / %q", b, mime)
	}
	if _, _, err := decodeDataURL("https://example.com/x.png"); err == nil {
		t.Error("expected error for non-data URL")
	}
}
