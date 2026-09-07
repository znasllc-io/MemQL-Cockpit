package modelcall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The four modality calls (engine memql#5137, record D4).
//
// Vision and transcription go through the OpenAI-compatible surface
// Ollama already exposes -- image parts on a chat message, `input_audio`
// for audio -- speech through a Kokoro runtime, and image generation
// through Ollama's own route. One transport, because the stream, the
// credential and the ledger are already there; the alternative was a
// second transport per modality.
//
// THE PAYLOADS HAVE NOWHERE TO TRAVEL YET, and that is the whole reason
// this file is separate from the dispatch. At the pin ModelCallMessage
// is {role, content} with no image parts, ModelCallDelta and
// ModelCallEnd carry only strings, and embedding_input is []string --
// so a vision call has nowhere to receive an image and speak and image
// have nowhere to return one. Everything below is the RUNTIME half:
// built, tested against a fake, and reached through payloadFor in
// session.go, which is the single place the wire's fields will be read
// when memql#5137 lands.

// ImagePart is one image handed to a vision call.
//
// Bytes plus a media type, never a URL. A worker that fetched a URL
// would be making an outbound request on behalf of whoever composed the
// prompt, from inside somebody's home network -- the exact shape the
// http tool's block_private_net policy exists to prevent, arriving
// through a door that has no policy at all.
type ImagePart struct {
	MediaType string
	Data      []byte
}

// dataURL renders the part the way both surfaces accept an inline
// image. base64 rather than a multipart upload because the chat route
// is JSON end to end and this is what its image_url field takes.
func (p ImagePart) dataURL() string {
	media := strings.TrimSpace(p.MediaType)
	if media == "" {
		// A media type the runtime cannot read is worse than a guess it
		// can: every runtime here decodes PNG, and a data URL with an
		// empty type is rejected by the URL parser before any decoder
		// sees it.
		media = "image/png"
	}
	return "data:" + media + ";base64," + base64Of(p.Data)
}

// VisionRequest is a chat turn carrying images.
type VisionRequest struct {
	Model    string
	Messages []Message
	// Images ride the LAST user turn, which is where every
	// OpenAI-compatible server expects them and the only placement that
	// survives a multi-turn conversation: attaching them to the first
	// turn asks the model about an image several turns of context away
	// from the question.
	Images []ImagePart
	Params Params
}

// TranscribeRequest is audio in, text out.
type TranscribeRequest struct {
	Model string
	// Audio is the raw bytes. Format is the container ("wav", "mp3"),
	// which the OpenAI-compatible input_audio field requires by name --
	// it is not sniffed from the bytes, because a wrong guess produces
	// a transcript of noise rather than an error.
	Audio  []byte
	Format string
	// Prompt is optional context ("this recording uses these product
	// names"), passed through when the caller supplies one.
	Prompt string
}

// TranscribeResult is what came back.
type TranscribeResult struct {
	Text  string
	Usage Usage
}

// SpeakRequest is text in, audio out.
type SpeakRequest struct {
	Model string
	Text  string
	// Voice names the speaker. Empty lets the runtime choose its own
	// default rather than this code inventing one: a voice id is a
	// property of the model that is installed, and a name from here
	// that the runtime does not have is a call that fails.
	Voice string
	// Format is the container asked for ("mp3", "wav"). Empty means the
	// runtime's default.
	Format string
}

// SpeakResult is the generated audio.
type SpeakResult struct {
	Audio     []byte
	MediaType string
	Usage     Usage
}

// ImageRequest is a prompt in, image bytes out.
type ImageRequest struct {
	Model  string
	Prompt string
}

// ImageResult is the generated image.
type ImageResult struct {
	Images []ImagePart
	Usage  Usage
}

// -----------------------------------------------------------------------------
// Vision -- the OpenAI-compatible chat route with image parts
// -----------------------------------------------------------------------------

// Vision runs a chat turn carrying images.
//
// It reuses the CHAT route rather than a separate one, because that is
// what the surface offers: a vision call is a chat call whose last user
// turn has a content ARRAY instead of a string. The result streams like
// any other generation, so the caller's emit sees tokens as they arrive.
func (c *openAIClient) Vision(ctx context.Context, req VisionRequest, emit Emit) (Result, error) {
	if len(req.Images) == 0 {
		return Result{}, fmt.Errorf("vision: no image was supplied")
	}
	return c.Chat(ctx, ChatRequest{
		Model:    req.Model,
		Messages: attachImages(req.Messages, req.Images),
		Params:   req.Params,
	}, emit)
}

// attachImages marks the last user turn as carrying images.
//
// The marker is a sentinel on the Message rather than a second
// parameter threaded through openAIMessages, so the mapping stays one
// function with one shape. A conversation with NO user turn gets one
// appended: an image with no turn to belong to would otherwise be
// dropped silently, and a vision call that quietly became a text call
// answers confidently about nothing.
func attachImages(in []Message, images []ImagePart) []Message {
	out := append([]Message(nil), in...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "user" {
			out[i].Images = images
			return out
		}
	}
	return append(out, Message{Role: "user", Images: images})
}

// -----------------------------------------------------------------------------
// Transcription -- input_audio on the same chat route
// -----------------------------------------------------------------------------

// Transcribe sends audio and returns the text.
//
// Through the CHAT route with an `input_audio` part rather than through
// /audio/transcriptions, and the reason is which servers implement
// which: Ollama's OpenAI-compatible surface serves audio-capable models
// (gemma4:e4b) on /chat/completions and does not implement the
// transcription route at all. A dedicated transcriber declared as its
// own runtime is reached the same way, because that is the route this
// cockpit's declared-runtime contract already promises.
func (c *openAIClient) Transcribe(ctx context.Context, req TranscribeRequest) (TranscribeResult, error) {
	if len(req.Audio) == 0 {
		return TranscribeResult{}, fmt.Errorf("transcribe: no audio was supplied")
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		// Named rather than sniffed: a wrong container guess produces a
		// transcript of noise where a stated one produces an error the
		// caller can read.
		return TranscribeResult{}, fmt.Errorf("transcribe: the audio format was not stated")
	}

	content := []map[string]any{{
		"type": "input_audio",
		"input_audio": map[string]any{
			"data":   base64Of(req.Audio),
			"format": format,
		},
	}}
	if p := strings.TrimSpace(req.Prompt); p != "" {
		content = append(content, map[string]any{"type": "text", "text": p})
	}

	var text strings.Builder
	res, err := c.chatRaw(ctx, map[string]any{
		"model":          req.Model,
		"messages":       []map[string]any{{"role": "user", "content": content}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}, func(chunk string) error {
		text.WriteString(chunk)
		return nil
	})
	if err != nil {
		return TranscribeResult{}, err
	}
	return TranscribeResult{Text: text.String(), Usage: res.Usage}, nil
}

// -----------------------------------------------------------------------------
// Speech -- a Kokoro runtime's /audio/speech
// -----------------------------------------------------------------------------

// kokoroClient serves text to speech.
//
// It is a separate client rather than a method on the OpenAI one
// because it is a separate SERVICE: Ollama does not serve speech at
// all, so a machine that speaks has a second runtime installed and
// advertises `runtime:kokoro` for it. The route it exposes is
// OpenAI-shaped (/v1/audio/speech), which is why the request looks
// familiar; the endpoint behind it is not the same process.
type kokoroClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewKokoroClient builds a speech client for a runtime's base URL.
func NewKokoroClient(baseURL, apiKey string, httpClient *http.Client) *kokoroClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &kokoroClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  apiKey,
		http:    httpClient,
	}
}

// Speak generates audio.
//
// The response is BYTES, not JSON: /audio/speech answers with the audio
// file itself and states its container in Content-Type. Reading the
// header rather than assuming the requested format is what keeps the
// result honest when a runtime silently serves wav for an mp3 request,
// which several do.
func (c *kokoroClient) Speak(ctx context.Context, req SpeakRequest) (SpeakResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return SpeakResult{}, fmt.Errorf("speak: no text was supplied")
	}
	body := map[string]any{"model": req.Model, "input": req.Text}
	if v := strings.TrimSpace(req.Voice); v != "" {
		body["voice"] = v
	}
	if f := strings.TrimSpace(req.Format); f != "" {
		body["response_format"] = f
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return SpeakResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/audio/speech", bytes.NewReader(raw))
	if err != nil {
		return SpeakResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return SpeakResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return SpeakResult{}, fmt.Errorf("kokoro: /audio/speech returned %d%s", resp.StatusCode, readErrorBody(resp))
	}

	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return SpeakResult{}, err
	}
	if len(audio) == 0 {
		// A 200 with an empty body is not success. The same trap
		// /memql/query and Ollama's /api/pull set: the status says the
		// request was accepted, and the absence of bytes is the failure.
		return SpeakResult{}, fmt.Errorf("kokoro: /audio/speech answered 200 with no audio")
	}
	media := resp.Header.Get("Content-Type")
	if media == "" {
		media = "audio/mpeg"
	}
	return SpeakResult{Audio: audio, MediaType: media}, nil
}

// -----------------------------------------------------------------------------
// Image generation -- Ollama's own route
// -----------------------------------------------------------------------------

// GenerateImage runs an image-generation model.
//
// Ollama answers /api/generate with base64 images in an `images` array
// for a model whose capabilities include `image`. The native route
// rather than an OpenAI-shaped one, for the same reason inference.Pull
// uses /api/pull: this is the surface the runtime actually implements,
// and the compatibility layer does not carry image generation at all.
func (c *ollamaClient) GenerateImage(ctx context.Context, req ImageRequest) (ImageResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return ImageResult{}, fmt.Errorf("image: no prompt was supplied")
	}
	resp, err := c.post(ctx, "/api/generate", map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
		"stream": false,
	})
	if err != nil {
		return ImageResult{}, err
	}
	defer resp.Body.Close()

	var body struct {
		Images          []string `json:"images"`
		PromptEvalCount int64    `json:"prompt_eval_count"`
		EvalCount       int64    `json:"eval_count"`
		Model           string   `json:"model"`
		Error           string   `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ImageResult{}, fmt.Errorf("image: the runtime's response could not be read: %w", err)
	}
	// A 200 CARRYING AN ERROR is the shape Ollama uses, so the status is
	// checked and then the body is checked again.
	if e := strings.TrimSpace(body.Error); e != "" {
		return ImageResult{}, fmt.Errorf("image: %s", e)
	}
	if len(body.Images) == 0 {
		return ImageResult{}, fmt.Errorf("image: the runtime returned no image")
	}

	out := ImageResult{Usage: Usage{
		InputTokens:  body.PromptEvalCount,
		OutputTokens: body.EvalCount,
		Known:        body.PromptEvalCount > 0 || body.EvalCount > 0,
		Model:        body.Model,
	}}
	for _, encoded := range body.Images {
		data, err := decodeBase64(encoded)
		if err != nil {
			return ImageResult{}, fmt.Errorf("image: the runtime returned an image that could not be decoded: %w", err)
		}
		// Ollama states no media type for these; PNG is what its image
		// models emit. Named as an assumption rather than read from
		// somewhere it is not written.
		out.Images = append(out.Images, ImagePart{MediaType: "image/png", Data: data})
	}
	return out, nil
}
