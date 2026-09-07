package modelcall

import (
	"fmt"
	"strings"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"
)

// payload.go is THE PROTO SEAM for the four modality kinds
// (engine memql#5137, record D4).
//
// payloadFor is the single place the wire's modality fields are read,
// and buildModalityEnd is the single place its modality bytes are
// written. That confinement is the whole point: when memql#5137 lands,
// this file is a field mapping and nothing else in the package moves.
// The alternative -- reading the fields wherever they happened to be
// needed -- is what turns a pin bump into a rewrite.
//
// WHY THERE IS NOTHING TO READ YET. At the current pin (5c4f6ae9) and
// on engine main alike, ModelCallStart carries `messages` of
// {role, content} with no image parts, `embedding_input` of []string
// with nowhere for audio, and ModelCallDelta / ModelCallEnd carry
// `content string` with nowhere for audio or image bytes. So a vision
// call cannot receive its image and a speak call cannot return its
// audio, whatever this cockpit does.
//
// The KIND selector needs none of that and is live today: kind is a
// plain string on ModelCallStart, and the label flags ride the existing
// labels map. So this cockpit advertises the four modalities, admits
// the four kinds, checks the advertised flag -- and then refuses by
// name, which is the honest answer rather than a text completion
// pretending to be a vision answer.

// Payload is a modality call's input, once the wire can carry one.
type Payload struct {
	// Images for a vision call.
	Images []ImagePart
	// Audio and AudioFormat for a transcription call.
	Audio       []byte
	AudioFormat string
	// Text for a speak call, and Prompt for an image call. Both would
	// arrive on fields ModelCallStart does not have; the existing
	// `messages` cannot stand in, because a caller sending prose in a
	// message is asking for a chat completion and a caller sending it
	// as speech input is not.
	Text   string
	Prompt string
}

// payloadFor reads the modality payload off the start envelope.
//
// It reports (Payload{}, false) at every engine version that has no
// modality fields, which is every engine version today. When memql#5137
// lands the body becomes the mapping:
//
//	case KindVision:     imagesFrom(start.GetImages())
//	case KindTranscribe: start.GetAudio(), start.GetAudioFormat()
//	case KindSpeak:      start.GetText()
//	case KindImage:      start.GetPrompt()
//
// It does NOT fall back to the existing string fields, and that is the
// decision worth defending: `messages` could plausibly carry a speak
// call's text and `embedding_input` could plausibly carry base64 audio,
// and either would be this cockpit inventing a wire the engine does not
// speak. The engine would then have to either adopt an encoding nobody
// designed or break a machine already using it.
func payloadFor(start *memqlv1.ModelCallStart) (Payload, bool) {
	_ = start
	return Payload{}, false
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
	return strings.TrimSpace(fmt.Sprintf(
		"this cockpit serves %s and the call carried no %s payload; "+
			"the field does not exist on ModelCallStart at this engine version", word, word))
}
