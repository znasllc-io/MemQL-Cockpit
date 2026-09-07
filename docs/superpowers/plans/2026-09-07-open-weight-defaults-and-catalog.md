# Open-weight defaults -- cockpit half, Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `setup --inference` pulls the machine's recommended open-weight set instead of the August floor, and this machine advertises and serves the four new modalities -- vision, transcription, speech and images -- through the runtimes it actually has.

**Architecture:** The default pair moves from a constant to a function of the machine class computed by `internal/worker/hardware` (the sibling plan's Task 1), so a 64 GB machine and a 16 GB machine are set up differently by the same command. The four modality flags join the existing `model:<id>` label grammar, which needs no proto change because labels are a `map<string,string>` on Register today. The four serving paths are built on the runtime clients that already exist, each behind one named seam for the payload the wire cannot yet carry.

**Tech Stack:** Go, single module. No new dependencies.

**Spec:** the engine repository's `docs/superpowers/specs/2026-09-07-open-weight-defaults-and-catalog-design.md`, sections 3 (D2, D4, D5) and 7 (tasks 1 and 2, cockpit half). Engine epic znasllc-io/memql#5137.

## Global Constraints

- **The four KINDS and the four FLAGS need no proto change; the four PAYLOADS do.** `ModelCallStart.kind` is a plain `string` and `Register.labels` is a `map<string,string>`, so both ship now. But at the pin `ModelCallMessage` is `{role, content}` with no image parts, `ModelCallDelta.content` and `ModelCallEnd.content` are strings with no bytes, and `embedding_input` is `[]string` -- so vision has nowhere to receive an image and speak and image have nowhere to return one. Build the client half, name the seam, refuse cleanly.
- **A flag is never advertised without a probe confirming it** (issue #395's own acceptance criterion, and the `models` package's fail-closed rule). Never inferred from a model id.
- **Emission order in the label is load-bearing.** `Attributes.String` must produce byte-identical output for an unchanged inventory or every reconnect rewrites the registration row. The four new keys go AFTER `tools`.
- **False is ABSENT.** A flag that is false is omitted from the label entirely; there is no `vision=0`.
- **The engine parses none of these keys at the current pin.** memql#5137 has not merged. This repository DEFINES the spelling and `TestWireContract` says so rather than pretending to transcribe -- safe only in this direction, because the engine's parser skips a key it does not know.
- **The sentences are the product.** Verbatim assertions, sentence case, no ALL-CAPS.

---

### Task 1: The recommended set replaces the August pair (#394)

**Files:**
- Modify: `internal/worker/inference/plan.go` (`defaultModels` -> `RecommendedSet`, `Host.Hardware`, `Plan.MachineClass`)
- Create: `internal/worker/inference/recommend.go`, `recommend_test.go`
- Modify: `internal/worker/inference/plan_test.go`
- Modify: `internal/worker/inference_cmd.go` (the preamble's Models line, the closing block)
- Modify: `internal/worker/cli.go` (the `--model` help text)
- Test: `internal/worker/inference_cmd_test.go`

**Interfaces:**
- Consumes: `hardware.Class(hardware.Inventory) string` and `hardware.Inventory` from the sibling plan's Task 1.
- Produces: `inference.RecommendedSet(class string) []string`; `Plan.DefaultModels` keeps its name and meaning; `Plan.MachineClass string`.

- [ ] **Step 1: Write the failing default-pair test**

The acceptance criterion is "the default pair asserted by name", so it is asserted by name.

```go
func TestRecommendedSetByClass(t *testing.T) {
	for _, tc := range []struct {
		class string
		want  []string
	}{
		// The record's D2 table names sets at 16, 32 and 64 only. A
		// class between two named sets takes the largest named set at
		// or below it -- 24 is a 16 that has not earned the 27B yet.
		{"unsupported", []string{"qwen3.5:9b", "qwen3-embedding:0.6b"}},
		{"16", []string{"qwen3.5:9b", "qwen3-embedding:0.6b"}},
		{"24", []string{"qwen3.5:9b", "qwen3-embedding:0.6b"}},
		{"32", []string{"qwen3.5:9b", "qwen3.8:27b", "qwen3-embedding:0.6b"}},
		{"64", []string{"qwen3.5:9b", "qwen3.8:27b", "qwen3.5:35b", "gemma4:26b", "qwen3-embedding:4b"}},
		{"128", []string{"qwen3.5:9b", "qwen3.8:27b", "qwen3.5:35b", "gemma4:26b", "qwen3-embedding:4b"}},
		{"", []string{"qwen3.5:9b", "qwen3-embedding:0.6b"}},
	} {
		t.Run(tc.class, func(t *testing.T) {
			if got := inference.RecommendedSet(tc.class); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RecommendedSet(%q) =\n%v\nwant\n%v", tc.class, got, tc.want)
			}
		})
	}
}

// The pull ORDER is the set's order and it is not incidental: the
// closing block reads back what arrived in the order it was asked for,
// and an operator comparing the two should not have to hunt. The
// embedder goes LAST because it is the smallest and the one whose
// absence is least visible -- a run interrupted halfway leaves the
// machine with a general model rather than with only an embedder.
func TestRecommendedSetPutsTheEmbedderLast(t *testing.T) {
	for _, class := range []string{"16", "24", "32", "64", "128"} {
		set := inference.RecommendedSet(class)
		if !strings.Contains(set[len(set)-1], "embedding") {
			t.Fatalf("class %s ends with %q, want the embedder last", class, set[len(set)-1])
		}
	}
}
```

- [ ] **Step 2: Run it, watch it fail**

Run: `go test ./internal/worker/inference/ -run TestRecommendedSet -v`
Expected: FAIL, "undefined: inference.RecommendedSet".

- [ ] **Step 3: Write `recommend.go`**

```go
// The recommended set, by machine class (record D2, verified against
// the Ollama library on 2026-09-07 by the engine's record).
//
// WHY THIS IS A FUNCTION AND NOT A CONSTANT. The August pair
// (llama3.1:8b, nomic-embed-text) was a floor chosen when one pair had
// to serve every machine. It is now the wrong answer in both
// directions: it under-uses a 64 GB machine, and it predates models
// that carry tools, thinking, structured output and vision in one
// record -- which is the whole reason the fleet can rank on `params`
// at all.
//
// A CLASS BELOW 16 STILL GETS A SET, and that is deliberate. The class
// gates the RECOMMENDATION, never the machine: the hardware floor
// already decided whether this machine serves at all, and a Linux box
// with 8 GB of VRAM meets that floor and can hold a 9B model. Handing
// it an empty set would be this command refusing a machine that works.
func RecommendedSet(class string) []string {
	switch class {
	case "64", "128":
		return []string{"qwen3.5:9b", "qwen3.8:27b", "qwen3.5:35b", "gemma4:26b", "qwen3-embedding:4b"}
	case "32":
		return []string{"qwen3.5:9b", "qwen3.8:27b", "qwen3-embedding:0.6b"}
	default:
		// 16, 24, unsupported, and an unrecognised or empty class. An
		// unknown class takes the SMALLEST set rather than none: the
		// failure of guessing small is a slower model, and the failure
		// of guessing large is a pull that fills somebody's disk.
		return []string{"qwen3.5:9b", "qwen3-embedding:0.6b"}
	}
}
```

- [ ] **Step 4: Run to green, then wire it into `Decide`**

Run: `go test ./internal/worker/inference/ -run TestRecommendedSet -v` -> PASS.

`Host` grows `Hardware hardware.Inventory`; `Gather` fills it from `hardware.Local`; `Decide` sets `p.MachineClass = hardware.Class(h.Hardware)` and `p.DefaultModels = RecommendedSet(p.MachineClass)`. `defaultModels()` is deleted with its last caller -- leaving it would leave the August pair reachable, which is the shape of a constant that comes back.

`Decide` stays a pure function of `Host`: the class is computed from a field already on it, not probed inside.

- [ ] **Step 5: Write the failing preamble test**

The preamble already prints Hardware / Runtime / Models. It gains the class, because "why these models" is the question the new default raises and an operator who cannot answer it will pass `--model` forever.

```go
func TestPreambleNamesTheClassAndTheSet(t *testing.T) {
	out := runInference(t, hostAt64GB())
	for _, want := range []string{
		"  Hardware   apple silicon, 64 GB, macOS 15 -- meets the floor",
		"  Class      32 -- 48 GB usable, 75% of 64 GB unified",
		"  Models     qwen3.5:9b, qwen3.8:27b, qwen3-embedding:0.6b",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("preamble missing %q:\n%s", want, out)
		}
	}
}

// --model still wins, and the Class line still appears -- it is a fact
// about the machine, not a justification for the default.
func TestModelFlagStillOverridesTheRecommendedSet(t *testing.T) {
	out := runInference(t, hostAt64GB(), "--model", "phi4:14b")
	if !strings.Contains(out, "  Models     phi4:14b") {
		t.Fatalf("--model did not override:\n%s", out)
	}
	if strings.Contains(out, "qwen3.5:9b") {
		t.Fatalf("the recommended set leaked into a --model run:\n%s", out)
	}
}
```

- [ ] **Step 6: Implement the Class line**

`s.field("Class", classLine(h))`, rendered from `hardware.Class` and `hardware.UsableBytes` so the figure and the class can never disagree. Below the smallest class the line reads `unsupported -- 6 GB usable, below the smallest class (16 GB)` and the Models line still names a set, because the machine still gets one.

- [ ] **Step 7: Assert `--non-interactive` is unchanged**

The criterion says it "still refuses to install anything unasked". It does -- `ensureRuntime` passes a nil consent, which `InstallRuntime` reads as no. Add the regression test rather than trusting it, because this task changed the model list that flows through the same function:

```go
func TestNonInteractiveStillInstallsNothingWithTheRecommendedSet(t *testing.T) {
	out, code := runInference2(t, hostNeedingInstall(), "--non-interactive")
	if code != SetupExitRefused {
		t.Fatalf("exit = %d, want %d", code, SetupExitRefused)
	}
	if !strings.Contains(out, "Nothing was installed: --non-interactive cannot answer that question.") {
		t.Fatalf("output:\n%s", out)
	}
}
```

- [ ] **Step 8: Update the help text and commit**

`cli.go`'s two `--model` strings name the pair. They become "(default: the recommended set for this machine's class)" -- a fixed pair in help text that no longer describes any machine is worse than no default named at all.

```bash
go test ./internal/worker/... && git add -A && \
git commit -m "setup --inference: the recommended set for this machine, not the August pair"
```

---

### Task 2: The four modalities -- flags, probes and serving (#395)

**Files:**
- Modify: `internal/worker/models/models.go` (four attribute keys, `String`, `ParseAttributes`)
- Modify: `internal/worker/models/ollama.go` (the vision probe, the image probe)
- Modify: `internal/worker/models/discover.go` (`DeclaredModel` fields), `openai.go`
- Modify: `internal/worker/models/models_test.go` (`TestWireContract`)
- Create: `internal/worker/modelcall/modalities.go`, `modalities_test.go`
- Create: `internal/worker/modelcall/kokoro.go`
- Modify: `internal/worker/modelcall/session.go` (the kind switch, the payload seam)
- Modify: `internal/worker/models_cmd.go` (`attributeLine`)
- Test: `internal/worker/modelcall/session_test.go`

**Interfaces:**
- Produces: `models.Attributes{Vision, AudioIn, AudioOut, ImageGen bool}`; `modelcall.KindVision/KindTranscribe/KindSpeak/KindImage`; `modelcall.CodePayloadUnavailable`.
- Consumes: `modelcall.Client` exported by the sibling plan's Task 2 Step 1.

- [ ] **Step 1: Write the failing wire-contract test**

```go
// The cockpit DEFINES these four keys: memql#5137 has not merged, so
// there is nothing upstream to transcribe. The engine's parser skips a
// key it does not know, which is why a misspelling here raises nothing
// anywhere and is simply a fleet that never routes a vision turn.
func TestWireContractModalityKeys(t *testing.T) {
	full := models.Attributes{
		ContextWindow: 8192, StructuredOutput: true, Embeddings: true,
		MaxConcurrent: 4, Params: 9_000_000_000, Quant: "Q4_K_M", Tools: true,
		Vision: true, AudioIn: true, AudioOut: true, ImageGen: true,
	}
	want := "ctx=8192,structured=1,embeddings=1,max=4,params=9000000000,quant=Q4_K_M," +
		"tools=1,vision=1,audioin=1,audioout=1,imagegen=1"
	if got := full.String(); got != want {
		t.Fatalf("label =\n%q\nwant\n%q", got, want)
	}
	if got := models.ParseAttributes(want); !reflect.DeepEqual(got, full) {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// FALSE IS ABSENT. There is no vision=0: the engine reads a missing
// key as absent, and emitting a zero would make "this machine says no"
// and "this machine did not say" the same string on the wire.
func TestModalityFlagsAreOmittedWhenFalse(t *testing.T) {
	got := models.Attributes{ContextWindow: 8192}.String()
	for _, key := range []string{"vision", "audioin", "audioout", "imagegen"} {
		if strings.Contains(got, key) {
			t.Fatalf("%q appeared in a label that claims nothing: %q", key, got)
		}
	}
}

// The fingerprint MOVES. Every machine reconnects once on rollout --
// the cost D8 accepts, and the thing to expect on deploy rather than
// read as an incident.
func TestModalityFlagsChangeTheFingerprint(t *testing.T) {
	a := models.Attributes{ContextWindow: 8192}.String()
	b := models.Attributes{ContextWindow: 8192, Vision: true}.String()
	if a == b {
		t.Fatal("a vision claim must change the label")
	}
}
```

- [ ] **Step 2: Run it, watch it fail, add the four keys**

Run: `go test ./internal/worker/models/ -run TestWireContract -v` -> FAIL, "unknown field Vision".

Four consts (`attrVision = "vision"`, `attrAudioIn = "audioin"`, `attrAudioOut = "audioout"`, `attrImageGen = "imagegen"`), four fields with the package's absent-defaults doc comment, four `String` branches AFTER `tools`, four `ParseAttributes` cases on `parseAdvertisedBool`. Grow the `parts` slice capacity from 7 to 11 -- a stale capacity is not a bug, but a reader counts it against the branches.

- [ ] **Step 3: Write the failing probe tests**

```go
// Ollama ANSWERS the vision question: "vision" is a capability in
// /api/show alongside "tools" and "embedding". So this one flag is a
// real probe rather than a declaration, and it is claimed only when
// the runtime says so.
func TestOllamaProbeClaimsVisionFromTheCapabilityList(t *testing.T) {
	srv := fakeOllama(t, map[string][]string{
		"seeing:9b":  {"completion", "tools", "vision"},
		"blind:9b":   {"completion", "tools"},
	})
	inv := probeAt(t, srv.URL)
	if !inv.MustFind("seeing:9b").Vision {
		t.Fatal("a model whose runtime reports vision must advertise it")
	}
	if inv.MustFind("blind:9b").Vision {
		t.Fatal("vision claimed for a model whose runtime never said so")
	}
}

// The other three have NO Ollama capability to read, so the probe
// claims nothing and the operator's declaration is the only source.
// Guessing from a model id is the failure this asserts against: a
// "kokoro" in a name is not a runtime that answered.
func TestOllamaProbeNeverGuessesTheOtherThreeModalities(t *testing.T) {
	srv := fakeOllama(t, map[string][]string{
		"kokoro-82m":       {"completion"},
		"whisper-large-v3": {"completion"},
		"x/z-image-turbo":  {"completion"},
	})
	for _, id := range []string{"kokoro-82m", "whisper-large-v3", "x/z-image-turbo"} {
		m := probeAt(t, srv.URL).MustFind(id)
		if m.AudioIn || m.AudioOut || m.ImageGen {
			t.Fatalf("%s: a modality was claimed from the model's NAME: %+v", id, m.Attributes)
		}
	}
}

// A declared runtime's operator states them, exactly as they state
// tools and structured output today.
func TestDeclaredModelCarriesTheModalityFlags(t *testing.T) {
	rt := models.DeclaredRuntime{Name: "kokoro", BaseURL: "...", Models: []models.DeclaredModel{
		{ID: "kokoro-82m", AudioOut: true},
	}}
	...
}
```

- [ ] **Step 4: Implement the probes**

- `vision` -> `hasCapability(show.Capabilities, "vision")`. A real capability Ollama reports; the one honest probe of the four.
- `imagegen` -> `hasCapability(show.Capabilities, "image")` when the runtime reports it, and NOTHING otherwise. The refusal path names what was observed rather than asserting which platforms the vendor offers image generation on -- a claim this cockpit cannot verify and would be stale within a release.
- `audioin`, `audioout` -> declared only. `DeclaredModel` gains `audio_in`, `audio_out`, `vision`, `image_gen`, spelled as the LABEL spells them (like `params`/`quant`/`tools`, unlike `context_window`), so an operator comparing the Fleet page against `policy.yaml` reads one word in both places.
- **A declared entry wins wholesale over the probe** (`resolveDuplicates`). That already bit this repository once -- PR #392 found `params`/`quant`/`tools` parsed and dropped, which would have DELETED tool support through the documented escape hatch. Add all four to the same mapping in the same commit, and extend the existing regression test rather than writing a new one beside it.

- [ ] **Step 5: Write the failing per-kind serving tests**

One in-process test per kind against a fake runtime, which is the acceptance criterion:

```go
func TestVisionCallSendsImagePartsToTheOpenAIEndpoint(t *testing.T) { ... }
func TestTranscribeCallSendsInputAudio(t *testing.T)                 { ... }
func TestSpeakCallReachesTheKokoroRuntime(t *testing.T)              { ... }
func TestImageCallReachesOllamaImageGeneration(t *testing.T)         { ... }

// The kind is REFUSED when the machine never advertised the modality,
// with a code that names the fix -- the same gate structured output
// and tools already get, and for the same reason: the router only
// sends a kind to a machine that advertised it, so a silent downgrade
// here would defeat the gating that put it there.
func TestKindRefusedWhenTheModalityWasNotAdvertised(t *testing.T) {
	end := startCall(t, offering(models.Attributes{}), kind("vision"))
	if end.GetErrorCode() != modelcall.CodeModalityUnsupported {
		t.Fatalf("error_code = %q, want %q", end.GetErrorCode(), modelcall.CodeModalityUnsupported)
	}
}
```

- [ ] **Step 6: Write `modalities.go` and `kokoro.go`**

Vision and transcription go through the OpenAI-compatible surface Ollama already exposes (image parts in `content`; `input_audio` for audio), speech through a Kokoro runtime's `/v1/audio/speech`, images through Ollama's image generation. Each is a function on the exported `Client` with its request and response types, fully exercised against `httptest` servers.

- [ ] **Step 7: Extend the kind switch and name the payload seam**

```go
// CodePayloadUnavailable: the kind is served by this machine and the
// call carried no payload for it.
//
// THIS IS THE PROTO SEAM, and it is a refusal rather than a silent
// empty call on purpose. At the pin ModelCallMessage is {role,
// content} with no image parts and ModelCallDelta/End carry only
// strings, so there is nowhere for an image in or audio bytes out to
// travel. When memql#5137 lands, `payloadFor` reads the new fields and
// this code stops being reachable; until then a machine that answered
// a vision call with a text completion would report success for a
// generation that never saw the image.
const CodePayloadUnavailable = "payload_unavailable"

// payloadFor is the ONE place the wire's payload fields are read.
func payloadFor(start *memqlv1.ModelCallStart) (Payload, bool) {
	// memql#5137: images, audio in and audio bytes out land here.
	return Payload{}, false
}
```

The kind switch admits all six kinds, checks the advertised flag for the four new ones, then asks `payloadFor`. No arm is written for a message type that does not exist -- but the KIND does exist, so this arm is real and its refusal is the honest answer today.

- [ ] **Step 8: Extend `attributeLine` and commit**

`memql worker models` gains the four, in the same "not advertised" idiom the line already uses for tools and structured output, so an operator can see at a glance which modality is missing and whether it is missing because nothing was probed or because policy blocked it.

```bash
go test ./... && git add -A && \
git commit -m "modalities: vision, transcribe, speak and image -- advertised only when probed"
```

---

## Self-Review

**Spec coverage.** D2's cockpit sentence ("the cockpit's `--inference` default pair becomes qwen3.5:9b and qwen3-embedding:0.6b") -> Task 1, generalised to the whole per-class table because issue #394's title asks for "the set the engine names for the machine's class". D4's cockpit sentence (vision and transcription through the OpenAI-compatible endpoint, speech through Kokoro, images through Ollama; the four flags) -> Task 2. D5 (windowed streaming transcription) is the ENGINE's fleet-door behaviour, not the cockpit's -- the cockpit serves one transcribe call and returns the transcript; the five-second windowing lives on the engine's `Start/Chunk/End` door. Stated here so its absence is a decision rather than a gap. D1, D3, D6-D9 are engine-side by the record's own delivery section.

**Placeholders.** Step 5 and 6 of Task 2 name four tests without inlining all four bodies. That is the one place this plan compresses, and it is bounded: each is the same shape as the two fully-written tests above it, against an `httptest` server, asserting the request body the client sent. Write them out at implementation time.

**Type consistency.** `models.Attributes` gains four fields used identically in `String`, `ParseAttributes`, `attributeLine` and the declared-model mapping. `modelcall.Client` is the exported name from the sibling plan (Task 2 Step 1) and is a hard ordering dependency: this task cannot land before that rename. `hardware.Class` and `hardware.UsableBytes` are the sibling plan's and are consumed unchanged.
