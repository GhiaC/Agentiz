// Package imageedit provides a built-in image editor backed by OpenRouter's
// image-output (multimodal) chat models — by default Google Gemini 2.5 Flash
// Image ("nano-banana"). It produces a function matching Agentize's
// engine.ImageEditorFunc signature, so it can be wired with
// Agentize.SetImageEditor / Agentize.UseOpenRouterImageEditor and drive the
// manage_files edit_image tool.
//
// OpenRouter exposes image generation/editing through the standard
// /chat/completions endpoint: send the source image as an image_url content
// part plus the edit instruction, with modalities ["image","text"]; the edited
// image comes back in choices[0].message.images[].image_url.url as a data URL.
package imageedit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
)

const (
	// DefaultBaseURL is the OpenRouter API base.
	DefaultBaseURL = "https://openrouter.ai/api/v1"
	// DefaultModel is Gemini 2.5 Flash Image ("nano-banana"), OpenRouter's
	// primary image-editing model.
	DefaultModel = "google/gemini-2.5-flash-image-preview"
	// DefaultTimeout bounds a single edit request.
	DefaultTimeout = 120 * time.Second
)

// OpenRouterConfig configures the editor.
type OpenRouterConfig struct {
	APIKey     string       // required: OpenRouter API key
	BaseURL    string       // default DefaultBaseURL
	Model      string       // default DefaultModel
	HTTPClient *http.Client // default: &http.Client{Timeout: DefaultTimeout}
	Referer    string       // optional HTTP-Referer header (OpenRouter attribution)
	Title      string       // optional X-Title header (OpenRouter attribution)
}

// OpenRouterEditor edits images via an OpenRouter image-output model.
type OpenRouterEditor struct {
	cfg OpenRouterConfig
}

// NewOpenRouter builds an editor, filling defaults.
func NewOpenRouter(cfg OpenRouterConfig) *OpenRouterEditor {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &OpenRouterEditor{cfg: cfg}
}

// Model returns the configured model name.
func (e *OpenRouterEditor) Model() string { return e.cfg.Model }

// EditImage edits image per instruction and returns the edited image plus its
// model/token usage. Its signature matches engine.ImageEditorFunc.
func (e *OpenRouterEditor) EditImage(image []byte, mimeType, instruction string) (*model.ImageEditResult, error) {
	start := time.Now()
	res, err := e.edit(image, mimeType, instruction)
	outLen := 0
	if res != nil {
		outLen = len(res.Data)
	}
	metrics.ImageEdit(e.cfg.Model, metrics.Status(err), time.Since(start), int64(len(image)), int64(outLen))
	if err != nil {
		log.Log.Warnf("[imageedit] ❌ edit failed | model=%s | in=%dB | instr_len=%d | dur=%s | err=%v",
			e.cfg.Model, len(image), len(instruction), time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	log.Log.Infof("[imageedit] ✅ edit ok | model=%s | in=%dB → out=%dB | mime=%s | tokens in=%d out=%d | dur=%s",
		e.cfg.Model, len(image), outLen, res.MIMEType, res.InputTokens, res.OutputTokens, time.Since(start).Round(time.Millisecond))
	return res, nil
}

func (e *OpenRouterEditor) edit(image []byte, mimeType, instruction string) (*model.ImageEditResult, error) {
	if e.cfg.APIKey == "" {
		return nil, fmt.Errorf("imageedit: OpenRouter API key not configured")
	}
	if len(image) == 0 {
		return nil, fmt.Errorf("imageedit: empty source image")
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	if strings.TrimSpace(instruction) == "" {
		instruction = "Edit this image."
	}

	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image)
	reqBody := chatRequest{
		Model:      e.cfg.Model,
		Modalities: []string{"image", "text"},
		Messages: []message{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: instruction},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("imageedit: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("imageedit: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	if e.cfg.Referer != "" {
		req.Header.Set("HTTP-Referer", e.cfg.Referer)
	}
	if e.cfg.Title != "" {
		req.Header.Set("X-Title", e.cfg.Title)
	}

	log.Log.Infof("[imageedit] → OpenRouter | model=%s | in=%dB | instr_len=%d", e.cfg.Model, len(image), len(instruction))

	resp, err := e.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imageedit: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // cap at 64MiB
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("imageedit: OpenRouter returned %s: %s", resp.Status, snippet(raw, 300))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("imageedit: decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("imageedit: OpenRouter error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("imageedit: no choices in response")
	}

	msg := parsed.Choices[0].Message
	for _, img := range msg.Images {
		if img.ImageURL.URL == "" {
			continue
		}
		data, mt, derr := decodeDataURL(img.ImageURL.URL)
		if derr != nil {
			return nil, fmt.Errorf("imageedit: decode image: %w", derr)
		}
		return &model.ImageEditResult{
			Data:         data,
			MIMEType:     mt,
			Model:        e.cfg.Model,
			InputTokens:  parsed.Usage.PromptTokens,
			OutputTokens: parsed.Usage.CompletionTokens,
		}, nil
	}

	// No image came back — surface any text the model returned (often a refusal).
	if txt := strings.TrimSpace(msg.Content); txt != "" {
		return nil, fmt.Errorf("imageedit: model returned no image (said: %s)", snippet([]byte(txt), 200))
	}
	return nil, fmt.Errorf("imageedit: model returned no image")
}

func (e *OpenRouterEditor) timeout() time.Duration {
	if e.cfg.HTTPClient != nil && e.cfg.HTTPClient.Timeout > 0 {
		return e.cfg.HTTPClient.Timeout
	}
	return DefaultTimeout
}

// --- wire types for the OpenRouter image-output chat API ---

type chatRequest struct {
	Model      string    `json:"model"`
	Modalities []string  `json:"modalities,omitempty"`
	Messages   []message `json:"messages"`
}

type message struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Images  []struct {
				Type     string   `json:"type"`
				ImageURL imageURL `json:"image_url"`
			} `json:"images"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// decodeDataURL decodes a "data:<mime>;base64,<payload>" URL into bytes + MIME.
func decodeDataURL(u string) ([]byte, string, error) {
	if !strings.HasPrefix(u, "data:") {
		return nil, "", fmt.Errorf("not a data URL")
	}
	rest := u[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return nil, "", fmt.Errorf("malformed data URL")
	}
	meta, payload := rest[:comma], rest[comma+1:]
	mimeType := meta
	base64Encoded := false
	if strings.HasSuffix(meta, ";base64") {
		mimeType = strings.TrimSuffix(meta, ";base64")
		base64Encoded = true
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	if !base64Encoded {
		return []byte(payload), mimeType, nil
	}
	if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
		return b, mimeType, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("base64 decode: %w", err)
	}
	return b, mimeType, nil
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
