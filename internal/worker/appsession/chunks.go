package appsession

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"

	"github.com/znasllc-io/memql-cockpit/internal/worker/harness"
)

// chunks.go owns everything that leaves the session: the ordered chunks,
// the transcript that becomes an artifact, the usage the app reported
// about itself, and the single End.

// chunkSendAttempts is how many times one chunk is retried before the
// session gives up on the stream.
const chunkSendAttempts = 3

// chunkRetryDelay is the pause between those attempts.
const chunkRetryDelay = 200 * time.Millisecond

// openTranscript starts the on-disk transcript.
//
// It goes to a file rather than a buffer because a run can be an hour
// long and its output is not bounded by anything the cockpit controls;
// holding all of it in memory would make a chatty agent an OOM on
// somebody's laptop. It lives inside the session scaffolding, so it is
// excluded from the produced-file sweep and removed with the session.
func (s *session) openTranscript(workspace string) error {
	path := filepath.Join(workspace, transcriptRel)
	if err := os.MkdirAll(filepath.Dir(path), configDirMode); err != nil {
		return fmt.Errorf("app session: transcript: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("app session: transcript: %w", err)
	}
	s.mu.Lock()
	s.transcript = f
	s.mu.Unlock()
	return nil
}

// emitChunk records one piece of output and sends it.
//
// THE CLASSIFICATION IS THE HARNESS'S. What used to stand here was a
// "does this line parse as JSON" test over raw stdout, which was a
// parser for a format nobody promised to keep; the harness reads each
// app's own protocol and hands over a chunk already labelled `event`,
// `text`, `tool`, `stdout` or `stderr`. Reclassifying anything here
// would put a second opinion in front of the first one.
//
// The seq is taken ONCE, before the first send attempt, and reused on
// every retry. The engine drops out-of-order and duplicate chunks rather
// than appending them, so renumbering a retry does not "fix" anything --
// it produces a gap the reader cannot see and a record that no longer
// matches what the app printed.
func (s *session) emitChunk(stream string, data []byte) error {
	return s.emit(stream, data, true)
}

// emit is emitChunk with the transcript cap made a decision rather than
// an assumption. See emitUncapped.
func (s *session) emit(stream string, data []byte, capped bool) error {
	if len(data) == 0 {
		return nil
	}
	clean := s.redact.apply(data)

	// The transcript artifact carries the FULL output, whatever the
	// engine's row-level cap is. That is the division of labour the
	// engine documents: it keeps a bounded transcript and marks it
	// truncated, and the complete one is expected to be an artifact.
	s.mu.Lock()
	transcript := s.transcript
	s.mu.Unlock()
	if transcript != nil {
		_, _ = fmt.Fprintf(transcript, "[%s] %s", stream, clean)
		if len(clean) > 0 && clean[len(clean)-1] != '\n' {
			_, _ = transcript.Write([]byte("\n"))
		}
	}

	if capped && s.transcriptCapReached(int64(len(clean))) {
		return nil
	}

	s.seqMu.Lock()
	s.seq++
	seq := s.seq
	s.seqMu.Unlock()

	var lastErr error
	for attempt := range chunkSendAttempts {
		if err := s.sender.SendAppSessionChunk(s.id, stream, clean, seq); err != nil {
			lastErr = err
			if attempt+1 < chunkSendAttempts {
				time.Sleep(chunkRetryDelay)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// transcriptCapReached applies limits.max_transcript_bytes to what is
// STREAMED, and says so once when it bites.
//
// The limit bounds what the engine will keep on the session row, so
// sending past it spends bandwidth on bytes that get dropped. Stopping
// silently, though, would leave a reader believing the run went quiet --
// so the cap emits one notice naming itself and pointing at the artifact
// that does have the rest.
func (s *session) transcriptCapReached(n int64) bool {
	max := s.start.GetLimits().GetMaxTranscriptBytes()
	if max <= 0 {
		return false
	}
	s.seqMu.Lock()
	if s.capped {
		s.seqMu.Unlock()
		return true
	}
	s.streamed += n
	if s.streamed <= max {
		s.seqMu.Unlock()
		return false
	}
	s.capped = true
	s.seq++
	seq := s.seq
	s.seqMu.Unlock()

	notice := fmt.Sprintf("[memql] live transcript truncated at limits.max_transcript_bytes (%d); "+
		"the full transcript is pushed to the Library as an artifact at the end of this session\n", max)
	_ = s.sender.SendAppSessionChunk(s.id, StreamStderr, []byte(notice), seq)
	return true
}

// emitUncapped sends a chunk that limits.max_transcript_bytes must not
// swallow.
//
// The cap bounds the TRANSCRIPT -- the running narration the engine
// keeps on the session row, whose complete form is pushed as an artifact
// anyway. A turn's structured ANSWER is not narration: it is the thing
// the caller asked for, it is a few hundred bytes, and losing it because
// the app was chatty would look exactly like an app that answered
// nothing. So the one chunk that carries a result is exempt, and nothing
// else is.
func (s *session) emitUncapped(stream string, data []byte) error {
	return s.emit(stream, data, false)
}

// recordTurn folds one turn's result into what the End will carry.
//
// Called once per turn, in the loop, before the next one starts, so
// there is no interleaving to reason about beyond the lock.
func (s *session) recordTurn(res harness.TurnResult) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()

	if ref := strings.TrimSpace(res.AppSessionRef); ref != "" {
		// The app's own session id, so a later kind=attach resumes THIS
		// conversation. A harness that lost it would have quietly turned
		// a conversation into a series of strangers.
		s.appRef = ref
	}

	// The LAST turn's answer, replacing rather than merging. A follow-up
	// that answered in prose must not inherit the previous turn's
	// structured result: the End describes the turn that just ended, and
	// carrying an older answer forward would report an answer to a
	// question nobody asked.
	s.result = res.ResultJSON

	if !res.Usage.Known {
		s.usageGaps++
		return
	}
	if s.usage == nil {
		s.usage = &memqlv1.AppSessionUsage{}
	}
	// SUMMED across turns, because the End reports the session and the
	// session is every turn in it. Taking only the last one would
	// under-report a five-turn conversation by four turns, in a ledger
	// somebody bills from.
	s.usage.InputTokens += res.Usage.InputTokens
	s.usage.OutputTokens += res.Usage.OutputTokens
	s.usage.CostUsd += res.Usage.CostUSD
}

// reportedUsage returns what the app told us, or an explicit
// known=false.
//
// The zero-with-known=false case is the important one and is built
// deliberately rather than left nil: it is the difference between "this
// run was free" and "nobody knows what this run cost", and only one of
// those is true.
//
// ONE SILENT TURN MAKES THE WHOLE SESSION UNKNOWN. A sum missing a turn
// is not a smaller measurement, it is an under-measurement recorded as a
// measurement -- the same failure as an estimate, arrived at politely.
// The counts still travel, because they cost nothing and the engine
// ignores them when known is false.
func (s *session) reportedUsage() *memqlv1.AppSessionUsage {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usage == nil {
		return &memqlv1.AppSessionUsage{Known: false}
	}
	return &memqlv1.AppSessionUsage{
		InputTokens:  s.usage.InputTokens,
		OutputTokens: s.usage.OutputTokens,
		CostUsd:      s.usage.CostUsd,
		Known:        s.usageGaps == 0,
	}
}

// pushOutputs uploads what the run produced plus the full transcript, and
// returns the artifact ids.
func (s *session) pushOutputs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	library, transcript, before := s.library, s.transcript, s.before
	s.transcript = nil
	s.mu.Unlock()

	transcriptPath := ""
	if transcript != nil {
		transcriptPath = transcript.Name()
		_ = transcript.Close()
	}
	if library == nil {
		return nil, nil
	}

	// The session's own context is already cancelled by the time this
	// runs on the cancel path, and the outputs of a cancelled run are
	// still worth keeping -- an hour of somebody's subscription went
	// into them. So the push gets its own bounded context.
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), libraryTimeout)
	defer cancel()

	var ids []string
	var failures []string

	workspace := strings.TrimSpace(s.start.GetWorkspace())
	if workspace != "" && before != nil {
		produced, skipped := collectProduced(workspace, before)
		for _, note := range skipped {
			// Never a silent truncation: a bounded list that reads as
			// complete is worse than a shorter one that says so.
			s.logger.Warn("app session output not pushed", "file", note)
			_ = s.emitChunk(StreamStderr, []byte("[memql] not pushed to the Library: "+note+"\n"))
		}
		for _, f := range produced {
			id, err := library.Push(pushCtx, f.path, artifactName(f.rel))
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			ids = append(ids, id)
		}
	}

	if transcriptPath != "" {
		body, err := os.ReadFile(transcriptPath)
		if err == nil {
			name := "app-session-" + sanitizeLedgerName(s.id) + "-transcript.log"
			if id, pushErr := library.PushBytes(pushCtx, body, name); pushErr != nil {
				failures = append(failures, pushErr.Error())
			} else {
				ids = append(ids, id)
			}
		}
		_ = os.Remove(transcriptPath)
		_ = os.Remove(filepath.Dir(transcriptPath))
	}

	if len(failures) > 0 {
		return ids, fmt.Errorf("app session: pushing outputs to the Library failed: %s",
			strings.Join(failures, "; "))
	}
	return ids, nil
}

// structuredResultChunk is the body of the final `event` chunk that
// carries a turn's structured answer. See resultEventType for why the
// answer travels this way and why the type word is namespaced.
type structuredResultChunk struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	Result    json.RawMessage `json:"result"`
}

// sendStructuredResult is THE SEAM for AppSessionEnd.result.
//
// One place holds the last turn's structured answer and one place emits
// it, so when memql#5096 lands the field the change is to assign it on
// the End here and stop emitting the chunk -- not to hunt for the value
// through the runner.
//
// It goes out BEFORE the End and after everything else, which makes it
// the session's last chunk: a chunk sent after the End is a chunk the
// engine has nowhere to put.
func (s *session) sendStructuredResult() {
	s.usageMu.Lock()
	result := s.result
	s.usageMu.Unlock()
	if len(result) == 0 {
		// No schema was asked for, or the app did not answer against
		// one. Nothing is synthesised: an empty object here reads
		// downstream as "the app answered nothing", which bills and
		// retries differently from "the app answered in prose".
		return
	}
	body, err := json.Marshal(structuredResultChunk{
		Type:      resultEventType,
		SessionID: s.id,
		Result:    json.RawMessage(result),
	})
	if err != nil {
		s.logger.Warn("the turn's structured result could not be encoded for the transcript", "error", err)
		return
	}
	if err := s.emitUncapped(StreamEvent, append(body, '\n')); err != nil {
		s.logger.Warn("the turn's structured result could not be sent", "error", err)
	}
}

// sendEnd closes the session on the wire, exactly once.
//
// exit_code is the app's REAL code. The engine reads a non-zero exit as a
// FAILED run rather than an ended one, so normalising a 2 to a 1 -- or
// worse, to a 0 -- misfiles the outcome in a record that is read back
// later by people deciding whether the thing worked.
func (s *session) sendEnd(code int, message string, artifacts []string) {
	s.sendStructuredResult()

	s.usageMu.Lock()
	ref := s.appRef
	s.usageMu.Unlock()
	if strings.TrimSpace(ref) == "" {
		ref = s.start.GetAppSessionRef()
	}

	end := &memqlv1.AppSessionEnd{
		SessionId:           s.id,
		ExitCode:            int32(code),
		Usage:               s.reportedUsage(),
		AppSessionRef:       ref,
		ProducedArtifactIds: artifacts,
		Error:               s.redact.apply2(message),
	}
	if err := s.sender.SendAppSessionEnd(end); err != nil {
		s.logger.Warn("app session end could not be sent", "error", err)
		return
	}
	s.logger.Info("app session ended",
		"exit_code", code,
		"artifacts", len(artifacts),
		"usage_known", end.GetUsage().GetKnown(),
		"error", message != "",
	)
}
