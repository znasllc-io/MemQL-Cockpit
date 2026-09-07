package modelcall

import (
	"fmt"
	"strings"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"
)

// payload.go is THE PROTO SEAM for the four modality kinds
// (engine memql#5137, record D4).
//
// payloadFor is the single place the wire's modality fields are read.
// That confinement is the whole point: when memql#5137 lands, this file
// is a field mapping and nothing else in the package moves. The
// alternative -- reading the fields wherever they happened to be needed
// -- is what turns a pin bump into a rewrite.
//
// WHY THERE IS NOTHING TO READ YET. At the current pin (5c4f6ae9) and on
// engine main alike, ModelCallStart carries `messages` of {role,
// content} with no image parts, `embedding_input` of []string with
// nowhere for audio, and ModelCallDelta / ModelCallEnd carry `content
// string` with nowhere for audio or image bytes. So a vision call cannot
// receive its image and a speak call cannot return its audio, whatever
// this cockpit does.
//
// The KIND selector needs none of that and is live today: kind is a
// plain string on ModelCallStart, and the label flags ride the existing
// labels map. So this cockpit advertises the four modalities, admits the
// four kinds, checks the advertised flag -- and then refuses by name,
// which is the honest answer rather than a text completion pretending to
// be a vision answer.
//
// THE SHAPE BELOW IS SETTLED, not guessed. It was agreed with the engine
// half of memql#5137 on 2026-09-07, so the mapping is written out rather
// than left as "some field, some day":
//
//	message ModelCallImage             { bytes data = 1; string media_type = 2; }
//	message ModelCallAudio             { bytes data = 1; string media_type = 2; int32 sample_rate_hz = 3; }
//	message ModelCallTranscriptSegment { double start_seconds = 1; double end_seconds = 2; string text = 3; }
//	message ModelCallSpeech            { string voice = 1; string format = 2; double speed = 3; bool speed_set = 4; }
//	message ModelCallImageRequest      { int32 width = 1; int32 height = 2; int32 count = 3; string format = 4; }
//
//	ModelCallMessage: repeated ModelCallImage images = 6;
//	ModelCallStart:   ModelCallAudio audio = 13;
//	                  ModelCallSpeech speech = 14;
//	                  ModelCallImageRequest image = 15;
//	ModelCallDelta:   repeated ModelCallTranscriptSegment segments = 6;
//	                  ModelCallAudio audio = 7;
//	ModelCallEnd:     repeated ModelCallTranscriptSegment segments = 9;
//	                  ModelCallAudio audio = 10;
//	                  repeated ModelCallImage images = 11;
//
// THREE CONSEQUENCES WORTH READING BEFORE EDITING ANYTHING HERE.
//
// Images belong to a TURN, not to the call: they land on
// ModelCallMessage rather than on ModelCallStart, because both runtimes
// attach them per message (Ollama's /api/chat takes `images` on the
// message; the OpenAI-compatible shape takes a content array on the
// message), and a repeated field on Start would need its own index back
// to a turn to say which one it belonged to.
//
// A speak call's TEXT and an image call's PROMPT are ordinary
// role="user" turns in `messages`. There is no new input field for
// either, and the knob messages carry only knobs -- so a speak call is
// messages + speech, and an image call is messages + image. That is why
// Payload below has no Text or Prompt field: reading them from anywhere
// but the messages would be a second place for the prompt to live.
//
// Bytes come back on BOTH Delta and End, following the rule `content`
// already follows: a worker that streamed leaves the End field empty and
// the engine assembles what it accepted, and a worker that did not
// stream puts the whole thing on End. So a streaming Kokoro answers on
// Delta.audio and a one-shot TTS answers on End.audio, with no second
// code path for either.

// Payload is a modality call's non-text input, once the wire carries
// one. The text half is always the messages.
type Payload struct {
	// Images are every image across every turn, in turn order, for a
	// vision call. The per-turn structure is preserved by the messages
	// themselves; this is the flat set the client half takes.
	Images []ImagePart
	// Audio is the kind="transcribe" input.
	Audio []byte
	// AudioMediaType is what the bytes ARE ("audio/wav"), as the wire
	// spells it. The OpenAI-compatible `input_audio.format` field wants
	// the bare container ("wav") instead, so audioFormatFor converts --
	// and it CONVERTS rather than guessing, because a wrong container
	// has the runtime decode the bytes as something they are not, and
	// the result is a confident transcript of noise rather than a
	// failure anybody notices.
	AudioMediaType string
	// Speech is the kind="speak" knobs.
	Speech SpeakRequest
	// Image is the kind="image" knobs.
	Image ImageRequest
}

// payloadFor reads the modality payload off the start envelope.
//
// It reports (Payload{}, false) at every engine version that has no
// modality fields, which is every engine version today. When memql#5137
// lands, the body becomes the mapping the comment above spells out.
//
// It does NOT fall back to the existing string fields, and that is the
// decision worth defending: `embedding_input` could plausibly carry
// base64 audio, and doing so would be this cockpit inventing a wire the
// engine does not speak. The engine would then have to either adopt an
// encoding nobody designed or break a machine already using it.
func payloadFor(start *memqlv1.ModelCallStart) (Payload, bool) {
	return readPayload(start)
}

// readPayload is the seam's body, held in a variable ONLY so the tests
// can drive the serving path that the wire cannot reach yet.
//
// Without it, every line of runModality -- the four client capability
// assertions, the media-type conversion, the transcript-as-delta rule,
// the prompt-from-the-last-user-turn rule -- would be code no test could
// execute, which is how a "mapping" that turns out to be a rewrite gets
// written. With it, the whole path is exercised today against fake
// runtimes and the only thing memql#5137 changes is this function's
// body.
var readPayload = func(start *memqlv1.ModelCallStart) (Payload, bool) {
	_ = start
	return Payload{}, false
}

// audioFormatFor turns a media type into the container name the
// OpenAI-compatible input_audio field wants.
//
// A media type this side does not recognise yields an empty string,
// which Transcribe refuses by name. That is the fail-closed direction,
// and it is the whole reason this conversion is a table rather than a
// string split on "/": "audio/x-wav" and "audio/wave" are both wav, and
// "audio/webm" is not "webm" by coincidence but by the same list.
func audioFormatFor(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg", "audio/opus":
		return "opus"
	case "audio/flac":
		return "flac"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/webm":
		return "webm"
	default:
		return ""
	}
}

// modalityResult is a modality call's non-text OUTPUT, on its way to
// ModelCallEnd. The text half rides the deltas, exactly as a chat
// generation's does.
type modalityResult struct {
	Segments       []TranscriptSegment
	Audio          []byte
	AudioMediaType string
	Images         []ImagePart
}

func (r modalityResult) empty() bool {
	return len(r.Segments) == 0 && len(r.Audio) == 0 && len(r.Images) == 0
}

// attachModalityResult is THE OTHER HALF OF THE SEAM: the single place
// a modality call's bytes are written to the wire.
//
// Nothing is written at this engine version, because ModelCallEnd has
// `content string` and no bytes anywhere. When memql#5137 lands this
// becomes the mapping its message list fixes:
//
//	end.Segments = segmentsProto(r.Segments)
//	end.Audio    = audioProto(r.Audio, r.AudioMediaType)
//	end.Images   = imagesProto(r.Images)
//
// Bytes may go on Delta instead, following the rule `content` already
// follows -- a worker that streamed leaves End's field empty and the
// engine assembles what it accepted. This cockpit's speech and image
// paths are one-shot, so they fill End's; a streaming transcriber would
// fill Delta's per window and leave the full set here.
func attachModalityResult(end *memqlv1.ModelCallEnd, r modalityResult) {
	if r.empty() {
		return
	}
	_ = end
}

// isModalityKind reports whether this kind is served by runModality.
func isModalityKind(kind string) bool {
	_, ok := modalityKinds[kind]
	return ok
}

// quoteAll renders a list of kinds for a refusal sentence. The refusal
// names every kind this worker serves, because a router that sent an
// unknown one is a version skew and the operator reading the report
// needs to see which side is behind.
func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// modalityUnavailableSentence is the refusal, in one place so the four
// kinds cannot drift into four different wordings.
func modalityUnavailableSentence(word string) string {
	return fmt.Sprintf(
		"this cockpit serves %s and the call carried no %s payload; "+
			"the field does not exist on ModelCallStart at this engine version", word, word)
}
