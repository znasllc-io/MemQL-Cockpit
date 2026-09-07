package modelcall

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"

	"github.com/znasllc-io/memql-cockpit/internal/worker/models"
)

// One in-process test per kind against a fake runtime, which is issue
// #395's acceptance criterion. No GPU, no Ollama, no Kokoro: every
// request body is captured and asserted, so what this cockpit SENDS is
// pinned rather than assumed.

// captured is the request a fake runtime received.
type captured struct {
	path string
	body map[string]any
}

func fakeOpenAI(t *testing.T, seen *[]captured, sse ...string) *openAIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, captured{path: r.URL.Path, body: body})
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range sse {
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return &openAIClient{baseURL: srv.URL, http: srv.Client()}
}

func chunk(content string) string {
	return `{"model":"m","choices":[{"delta":{"content":"` + content + `"}}]}`
}

// -----------------------------------------------------------------------------
// Vision
// -----------------------------------------------------------------------------

// A vision call is a CHAT call whose last user turn carries a content
// array. The image travels as a data URL, and the text part goes first.
func TestVisionSendsImagePartsOnTheLastUserTurn(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen, chunk("a cat"))

	res, err := c.Vision(context.Background(), VisionRequest{
		Model: "seeing:9b",
		Messages: []Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "What is this?"},
		},
		Images: []ImagePart{{MediaType: "image/png", Data: []byte("PNGDATA")}},
	}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != FinishStop {
		t.Fatalf("finish = %q", res.FinishReason)
	}
	if len(seen) != 1 || seen[0].path != "/chat/completions" {
		t.Fatalf("a vision call must use the chat route: %+v", seen)
	}

	msgs := seen[0].body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages", len(msgs))
	}
	// The system turn keeps a plain string; only the image-carrying
	// turn becomes an array.
	if _, ok := msgs[0].(map[string]any)["content"].(string); !ok {
		t.Fatalf("the system turn became an array: %v", msgs[0])
	}

	parts, ok := msgs[1].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("the user turn did not become a content array: %v", msgs[1])
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want text then image", len(parts))
	}
	first := parts[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "What is this?" {
		t.Fatalf("the text part must come first and keep the prompt: %v", first)
	}
	second := parts[1].(map[string]any)
	url := second["image_url"].(map[string]any)["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	if url != want {
		t.Fatalf("image url =\n  %q\nwant\n  %q", url, want)
	}
}

// The images ride the LAST user turn, not the first: attaching them to
// the first asks the model about an image several turns of context away
// from the question.
func TestVisionAttachesToTheLastUserTurn(t *testing.T) {
	got := attachImages([]Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	}, []ImagePart{{Data: []byte("x")}})

	if len(got[0].Images) != 0 {
		t.Error("images were attached to the first user turn")
	}
	if len(got[2].Images) != 1 {
		t.Error("images were not attached to the last user turn")
	}
}

// A conversation with NO user turn gets one, rather than dropping the
// image: a vision call that quietly became a text call answers
// confidently about nothing.
func TestVisionAppendsAUserTurnWhenThereIsNone(t *testing.T) {
	got := attachImages([]Message{{Role: "system", Content: "Be brief."}},
		[]ImagePart{{Data: []byte("x")}})
	if len(got) != 2 || got[1].Role != "user" || len(got[1].Images) != 1 {
		t.Fatalf("no user turn was appended for the image: %+v", got)
	}
}

// A vision call with no image is refused rather than served as a chat
// call. Serving it would answer confidently about an image nobody sent.
func TestVisionRefusesWithNoImage(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen)
	if _, err := c.Vision(context.Background(), VisionRequest{Model: "m"}, func(string) error { return nil }); err == nil {
		t.Fatal("want a refusal for a vision call with no image")
	}
	if len(seen) != 0 {
		t.Fatal("the runtime was called for a vision request with no image")
	}
}

// A part with no media type still produces a usable data URL. An empty
// type is rejected by the URL parser before any decoder sees it.
func TestImagePartWithNoMediaTypeStillEncodes(t *testing.T) {
	got := ImagePart{Data: []byte("x")}.dataURL()
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("dataURL = %q", got)
	}
}

// -----------------------------------------------------------------------------
// Transcription
// -----------------------------------------------------------------------------

func TestTranscribeSendsInputAudio(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen, chunk("hello "), chunk("world"))

	res, err := c.Transcribe(context.Background(), TranscribeRequest{
		Model: "gemma4:e4b", Audio: []byte("RIFFWAVE"), Format: "wav",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello world" {
		t.Fatalf("text = %q, want the concatenated stream", res.Text)
	}

	msgs := seen[0].body["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	audio := parts[0].(map[string]any)
	if audio["type"] != "input_audio" {
		t.Fatalf("part type = %v, want input_audio", audio["type"])
	}
	inner := audio["input_audio"].(map[string]any)
	if inner["format"] != "wav" {
		t.Fatalf("format = %v, want the STATED container", inner["format"])
	}
	if inner["data"] != base64.StdEncoding.EncodeToString([]byte("RIFFWAVE")) {
		t.Fatalf("audio data was not base64 of the bytes sent")
	}
}

// The format is STATED, never sniffed: a wrong container guess produces
// a transcript of noise where a stated one produces an error a caller
// can read.
func TestTranscribeRefusesWithNoStatedFormat(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen)
	if _, err := c.Transcribe(context.Background(), TranscribeRequest{Model: "m", Audio: []byte("x")}); err == nil {
		t.Fatal("want a refusal when the audio format was not stated")
	}
	if len(seen) != 0 {
		t.Fatal("the runtime was called with an unstated audio format")
	}
}

func TestTranscribeRefusesWithNoAudio(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen)
	if _, err := c.Transcribe(context.Background(), TranscribeRequest{Model: "m", Format: "wav"}); err == nil {
		t.Fatal("want a refusal for a transcription with no audio")
	}
}

// An optional prompt rides alongside the audio as a text part.
func TestTranscribeCarriesAnOptionalPrompt(t *testing.T) {
	var seen []captured
	c := fakeOpenAI(t, &seen, chunk("ok"))
	if _, err := c.Transcribe(context.Background(), TranscribeRequest{
		Model: "m", Audio: []byte("x"), Format: "wav", Prompt: "product names: MemQL",
	}); err != nil {
		t.Fatal(err)
	}
	parts := seen[0].body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want the audio and the prompt", len(parts))
	}
	if parts[1].(map[string]any)["text"] != "product names: MemQL" {
		t.Fatalf("the prompt was not carried: %v", parts[1])
	}
}

// -----------------------------------------------------------------------------
// Speech
// -----------------------------------------------------------------------------

func TestSpeakReachesTheKokoroRuntime(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFWAVEDATA"))
	}))
	t.Cleanup(srv.Close)

	res, err := NewKokoroClient(srv.URL, "", srv.Client()).Speak(context.Background(), SpeakRequest{
		Model: "kokoro-82m", Text: "hello", Voice: "af_bella", Format: "wav",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/audio/speech" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["input"] != "hello" || gotBody["voice"] != "af_bella" || gotBody["response_format"] != "wav" {
		t.Fatalf("body = %v", gotBody)
	}
	if string(res.Audio) != "RIFFWAVEDATA" {
		t.Fatalf("audio = %q", res.Audio)
	}
	// The media type comes from the RESPONSE HEADER, not from the
	// format asked for: several runtimes silently serve wav for an mp3
	// request, and a result that stated the request would be wrong.
	if res.MediaType != "audio/wav" {
		t.Fatalf("media type = %q, want the server's own header", res.MediaType)
	}
}

// An unset voice and format are OMITTED rather than defaulted here. A
// voice id is a property of the model that is installed, and a name
// invented by this code is a call that fails.
func TestSpeakOmitsAnUnsetVoiceAndFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte("audio"))
	}))
	t.Cleanup(srv.Close)

	if _, err := NewKokoroClient(srv.URL, "", srv.Client()).Speak(context.Background(),
		SpeakRequest{Model: "kokoro-82m", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["voice"]; ok {
		t.Error("an unset voice was sent")
	}
	if _, ok := gotBody["response_format"]; ok {
		t.Error("an unset format was sent")
	}
}

// A 200 WITH NO BYTES IS NOT SUCCESS -- the same trap /memql/query and
// Ollama's /api/pull set. A caller that trusted the status would hand
// back an empty audio file as a finished generation.
func TestSpeakRefusesAnEmptyTwoHundred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	_, err := NewKokoroClient(srv.URL, "", srv.Client()).Speak(context.Background(),
		SpeakRequest{Model: "m", Text: "hi"})
	if err == nil {
		t.Fatal("want a refusal for a 200 carrying no audio")
	}
	if !strings.Contains(err.Error(), "no audio") {
		t.Fatalf("err = %v", err)
	}
}

func TestSpeakRefusesWithNoText(t *testing.T) {
	if _, err := NewKokoroClient("http://127.0.0.1:1", "", nil).Speak(context.Background(),
		SpeakRequest{Model: "m"}); err == nil {
		t.Fatal("want a refusal for a speak call with no text")
	}
}

// -----------------------------------------------------------------------------
// Image generation
// -----------------------------------------------------------------------------

func TestImageReachesOllamaGenerate(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"model":"x/z-image-turbo","images":["` +
			base64.StdEncoding.EncodeToString([]byte("PNGBYTES")) +
			`"],"prompt_eval_count":12,"eval_count":0}`))
	}))
	t.Cleanup(srv.Close)

	res, err := (&ollamaClient{baseURL: srv.URL, http: srv.Client()}).GenerateImage(
		context.Background(), ImageRequest{Model: "x/z-image-turbo", Prompt: "a red cube"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/generate" {
		t.Fatalf("path = %q, want the NATIVE route", gotPath)
	}
	if gotBody["prompt"] != "a red cube" || gotBody["stream"] != false {
		t.Fatalf("body = %v", gotBody)
	}
	if len(res.Images) != 1 || string(res.Images[0].Data) != "PNGBYTES" {
		t.Fatalf("images = %+v", res.Images)
	}
	if !res.Usage.Known || res.Usage.InputTokens != 12 {
		t.Fatalf("usage = %+v, want what the runtime reported", res.Usage)
	}
}

// A 200 CARRYING AN ERROR is the shape Ollama uses, so the body is
// checked after the status.
func TestImageReadsAnErrorInsideATwoHundred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"model x/z-image-turbo not found"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := (&ollamaClient{baseURL: srv.URL, http: srv.Client()}).GenerateImage(
		context.Background(), ImageRequest{Model: "x/z-image-turbo", Prompt: "a cube"})
	if err == nil {
		t.Fatal("want a refusal for an error inside a 200")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want the runtime's own words", err)
	}
}

// A 200 with no images is a failure too: the other direction hands back
// an empty result as a finished generation.
func TestImageRefusesWhenNoImageCameBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","images":[]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (&ollamaClient{baseURL: srv.URL, http: srv.Client()}).GenerateImage(
		context.Background(), ImageRequest{Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("want a refusal when no image came back")
	}
}

// Usage is REPORTED, never inferred: a runtime that counted nothing
// leaves Known false, which the engine records as billing "unknown".
func TestImageUsageStaysUnknownWhenTheRuntimeCountedNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"images":["` + base64.StdEncoding.EncodeToString([]byte("x")) + `"]}`))
	}))
	t.Cleanup(srv.Close)

	res, err := (&ollamaClient{baseURL: srv.URL, http: srv.Client()}).GenerateImage(
		context.Background(), ImageRequest{Model: "m", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.Known {
		t.Fatalf("usage was claimed known with no counts: %+v", res.Usage)
	}
}

// -----------------------------------------------------------------------------
// Encoding
// -----------------------------------------------------------------------------

// Padded and unpadded both decode. Some runtimes strip trailing '='
// when they concatenate, and refusing that would fail a decode over
// punctuation.
func TestDecodeBase64ToleratesAMissingPad(t *testing.T) {
	raw := []byte("abcde")
	padded := base64.StdEncoding.EncodeToString(raw)
	unpadded := base64.RawStdEncoding.EncodeToString(raw)
	for _, in := range []string{padded, unpadded} {
		got, err := decodeBase64(in)
		if err != nil {
			t.Fatalf("decodeBase64(%q): %v", in, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("decodeBase64(%q) = %q", in, got)
		}
	}
	if _, err := decodeBase64("not base64 at all!!"); err == nil {
		t.Fatal("want an error for input that is not base64")
	}
}

// -----------------------------------------------------------------------------
// The dispatch gates
// -----------------------------------------------------------------------------

// startModality builds a start envelope for a modality kind. It carries
// no payload, because there is no field on ModelCallStart to put one in
// -- which is exactly the state the refusal below is about.
func startModality(model, kind string) *memqlv1.ModelCallStart {
	return &memqlv1.ModelCallStart{
		RequestId: "r1", Model: model, Kind: kind,
		Limits: &memqlv1.ModelCallLimits{TimeoutSeconds: 10, IdleTimeoutSeconds: 5, KeepaliveSeconds: 1},
	}
}

func endFor(t *testing.T, inv stubInventory, s *memqlv1.ModelCallStart) *memqlv1.ModelCallEnd {
	t.Helper()
	rec := newRecorder()
	m := NewManager(Options{Inventory: inv})
	m.Start(context.Background(), rec, s)
	return rec.wait(t)
}

// A modality the model never advertised is refused with a code that
// names the fix -- the same gate structured output and tools already
// get, and for the same reason: the router only sends a kind to a
// machine that advertised it, so a silent downgrade here would defeat
// the gating that put the call here.
func TestModalityKindRefusedWhenNotAdvertised(t *testing.T) {
	for _, tc := range []struct{ kind, word string }{
		{KindVision, "vision"},
		{KindTranscribe, "transcription"},
		{KindSpeak, "speech"},
		{KindImage, "image generation"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			inv := inventoryWith(ollamaModel("http://127.0.0.1:1", "m:9b", models.Attributes{}))
			end := endFor(t, inv, startModality("m:9b", tc.kind))

			if end.GetErrorCode() != CodeModalityUnsupported {
				t.Fatalf("error_code = %q, want %q", end.GetErrorCode(), CodeModalityUnsupported)
			}
			if !strings.Contains(end.GetError(), tc.word) {
				t.Fatalf("the refusal must name the modality: %q", end.GetError())
			}
		})
	}
}

// A model that DID advertise the modality gets past that gate and hits
// the payload seam, which is the honest answer at this engine version:
// the field does not exist, so no image ever arrived, and answering
// anyway would report success for a generation about nothing.
func TestAdvertisedModalityRefusesOnTheAbsentPayload(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		attrs models.Attributes
	}{
		{KindVision, models.Attributes{Vision: true}},
		{KindTranscribe, models.Attributes{AudioIn: true}},
		{KindSpeak, models.Attributes{AudioOut: true}},
		{KindImage, models.Attributes{ImageGen: true}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			inv := inventoryWith(ollamaModel("http://127.0.0.1:1", "m:9b", tc.attrs))
			end := endFor(t, inv, startModality("m:9b", tc.kind))

			if end.GetErrorCode() != CodePayloadUnavailable {
				t.Fatalf("error_code = %q, want %q -- got error %q",
					end.GetErrorCode(), CodePayloadUnavailable, end.GetError())
			}
			if !strings.Contains(end.GetError(), "does not exist on ModelCallStart") {
				t.Fatalf("the refusal must say WHY there is no payload: %q", end.GetError())
			}
		})
	}
}

// The two refusals are DIFFERENT CODES, and that is load-bearing: an
// unknown-to-this-machine modality is a stale advertisement or a policy
// change, where an absent payload is a version skew between the engine
// and this cockpit. The refusal report lists every machine considered
// and why, so two causes that send an operator to different places get
// two codes.
func TestModalityRefusalCodesAreDistinct(t *testing.T) {
	if CodeModalityUnsupported == CodePayloadUnavailable {
		t.Fatal("the two modality refusals must not share a code")
	}
	if CodeModalityUnsupported == CodeUnsupportedKind {
		t.Fatal("an unadvertised modality and an unknown kind must not share a code")
	}
}

// An unknown kind still refuses, and the refusal now names every kind
// this worker serves -- a router that sent an unknown one is a version
// skew, and the operator reading the report needs to see which side is
// behind.
func TestUnknownKindRefusalNamesEveryServedKind(t *testing.T) {
	inv := inventoryWith(ollamaModel("http://127.0.0.1:1", "m:9b", models.Attributes{}))
	end := endFor(t, inv, startModality("m:9b", "telepathy"))

	if end.GetErrorCode() != CodeUnsupportedKind {
		t.Fatalf("error_code = %q", end.GetErrorCode())
	}
	for _, kind := range ServedKinds() {
		if !strings.Contains(end.GetError(), `"`+kind+`"`) {
			t.Errorf("the refusal must name %q: %s", kind, end.GetError())
		}
	}
}

// Chat and embedding are untouched by the modality gate. The most
// likely way to break this change is to make an ordinary chat call take
// the modality path.
func TestChatAndEmbeddingAreNotModalityGated(t *testing.T) {
	for _, kind := range []string{KindChat, KindEmbedding} {
		if _, isModality := modalityKinds[kind]; isModality {
			t.Fatalf("%q was gated as a modality", kind)
		}
	}
}

// Every modality kind has exactly one entry in the table that maps it
// to a flag and a word. Three switch statements would be three places
// for a modality to be half-added; this is the one.
func TestEveryModalityKindIsInTheTable(t *testing.T) {
	want := []string{KindVision, KindTranscribe, KindSpeak, KindImage}
	if len(modalityKinds) != len(want) {
		t.Fatalf("%d entries for %d modality kinds", len(modalityKinds), len(want))
	}
	for _, kind := range want {
		entry, ok := modalityKinds[kind]
		if !ok {
			t.Fatalf("%q has no entry", kind)
		}
		if entry.Word == "" || entry.Advertised == nil {
			t.Fatalf("%q has an incomplete entry: %+v", kind, entry)
		}
	}
	// And ServedKinds lists all six, which is what the unknown-kind
	// refusal prints.
	if len(ServedKinds()) != len(want)+2 {
		t.Fatalf("ServedKinds = %v, want chat, embedding and the four modalities", ServedKinds())
	}
}
