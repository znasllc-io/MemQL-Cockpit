package inference

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// allow.go adds model ids to models.allow in policy.yaml.
//
// IT IS A TEXTUAL EDIT, AND THAT IS THE WHOLE DESIGN. The obvious
// implementation -- unmarshal the file, append to a slice, marshal it back
// -- CANNOT meet the requirement, and it fails in a way nobody notices in
// review: yaml.v3 re-serialises the document from its own node tree, which
// re-indents nested sequences, drops blank lines between sections, and
// re-quotes scalars to its own taste. An operator's policy.yaml has
// comments in it and a shape they chose. A command run to add one model
// must not be the thing that reformats the file describing what this
// machine is allowed to do.
//
// So the YAML parse here decides only WHAT to write -- which ids are
// already listed, where the models block is, which line the last entry
// sits on -- and every byte outside the lines this file inserts is carried
// through untouched. yaml.Node's Line and Column are what make that exact
// rather than a regular expression's guess at where a block begins.
//
// WHERE IT CANNOT DO THAT, IT REFUSES. A flow mapping, an allow key that
// is not a sequence, a root that is not a mapping: each of those could be
// handled by falling back to a re-serialisation, and the price would be an
// operator's comments deleted by a command they ran to add one model. A
// refusal names the line and names the ids, which costs them thirty
// seconds; the fallback costs them the file.
//
// MERGE, NEVER REPLACE. The ids already listed are the models this machine
// is serving right now, and a rewrite that dropped one takes it out of the
// fleet at the next reload -- with nothing in the output of the command
// that did it saying so.

const (
	// policyFileMode is the care worker.yaml gets (persistence.go).
	// policy.yaml holds no secret, but it decides what this machine will
	// execute, serve and upload, and that is not a file to leave writable
	// by anyone who can reach the disk.
	policyFileMode = 0o600
	policyDirMode  = 0o700
)

// ErrPolicyNotEditable is a policy.yaml this command will not rewrite.
// The wrapped message names the line and the ids, so the person can make
// the change by hand in the shape they already chose.
var ErrPolicyNotEditable = errors.New("this policy.yaml is written in a shape this command will not edit without reformatting the whole file")

// Allow merges model ids into models.allow, creating the file if needed.
//
// Nothing is written when there is nothing to add, which is the ordinary
// second run of `setup --inference`: an idempotent command that rewrote
// the file every time would churn its mtime, its mode and any backup
// watching it for no change at all.
func Allow(policyPath string, ids ...string) error {
	if strings.TrimSpace(policyPath) == "" {
		return errors.New("no policy.yaml path was given, so there is nowhere to record the model")
	}
	wanted := cleanIDs(ids)
	if len(wanted) == 0 {
		return nil
	}

	raw, err := os.ReadFile(policyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", policyPath, err)
	}

	updated, changed, err := mergeAllow(string(raw), wanted)
	if err != nil {
		return fmt.Errorf("%s: %w", policyPath, err)
	}
	if !changed {
		return nil
	}
	return writePolicyAtomic(policyPath, []byte(updated))
}

// mergeAllow returns the new file, and whether anything changed.
//
// Split out from Allow so the whole decision is a pure function of the
// bytes: every shape below is asserted on a string in the tests rather
// than on a file, which is what makes "byte for byte outside models.allow"
// something a test can actually claim.
func mergeAllow(body string, wanted []string) (string, bool, error) {
	lines := splitLines(body)

	var doc yaml.Node
	if strings.TrimSpace(body) != "" {
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			// The worker cannot read this file either, so it is already
			// broken. Overwriting it would delete whatever the operator is
			// halfway through fixing.
			return "", false, fmt.Errorf("it does not parse as YAML, so nothing was changed: %w", err)
		}
	}

	root := documentRoot(&doc)
	if root == nil {
		// Empty, missing, or comments only. The comments survive, because
		// the fresh block is appended to the lines that are there.
		return joinLines(appendModelsBlock(lines, wanted, len(lines) == 0)), true, nil
	}
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return notEditable(root.Line, wanted)
	}

	modelsKey, modelsVal := findKey(root, "models")
	if modelsKey == nil {
		return joinLines(appendModelsBlock(lines, wanted, false)), true, nil
	}
	if isNull(modelsVal) {
		return joinLines(insertLines(lines, modelsKey.Line, allowBlock(2, wanted))), true, nil
	}
	if modelsVal.Kind != yaml.MappingNode || modelsVal.Style&yaml.FlowStyle != 0 {
		return notEditable(modelsKey.Line, wanted)
	}

	// The indentation of models' own children, taken from a child rather
	// than assumed: a file indented with four spaces must stay indented
	// with four spaces.
	childIndent := 2
	if len(modelsVal.Content) > 0 {
		childIndent = modelsVal.Content[0].Column - 1
	}

	allowKey, allowVal := findKey(modelsVal, "allow")
	if allowKey == nil {
		return joinLines(insertLines(lines, modelsKey.Line, allowBlock(childIndent, wanted))), true, nil
	}

	add := missing(wanted, sequenceValues(allowVal))
	if len(add) == 0 {
		return body, false, nil
	}

	switch {
	case isNull(allowVal):
		// `allow:` with nothing under it -- what somebody leaves behind
		// after deleting the last model.
		return joinLines(insertLines(lines, allowKey.Line, items(allowKey.Column-1+2, add))), true, nil

	case allowVal.Kind == yaml.SequenceNode && allowVal.Style&yaml.FlowStyle != 0:
		return appendToFlowSequence(lines, allowKey.Line, add, wanted)

	case allowVal.Kind == yaml.SequenceNode:
		last := allowVal.Content[len(allowVal.Content)-1]
		if last.Kind != yaml.ScalarNode || last.Line < 1 || last.Line > len(lines) {
			return notEditable(allowKey.Line, wanted)
		}
		// The `- ` prefix is COPIED from the last entry rather than
		// rebuilt, so a list indented under its key and one indented level
		// with it both keep the shape they had.
		prefix := blockItemPrefix.FindStringSubmatch(lines[last.Line-1])
		if prefix == nil {
			return notEditable(last.Line, wanted)
		}
		out := make([]string, 0, len(add))
		for _, id := range add {
			out = append(out, prefix[1]+yamlScalar(id))
		}
		return joinLines(insertLines(lines, last.Line, out)), true, nil
	}
	return notEditable(allowKey.Line, wanted)
}

// blockItemPrefix captures the exact leading whitespace, dash and spacing
// of an existing sequence entry.
var blockItemPrefix = regexp.MustCompile(`^(\s*-\s+)\S`)

// flowSequence matches a single-line flow sequence and nothing else. A
// flow list spread over several lines, or one carrying a bracket inside a
// quoted value, falls through to the refusal -- getting either wrong
// writes a policy.yaml the worker can no longer parse, which takes the
// machine out of the fleet entirely.
var flowSequence = regexp.MustCompile(`^(\s*allow:\s*\[)([^\[\]]*)(\].*)$`)

func appendToFlowSequence(lines []string, line int, add, wanted []string) (string, bool, error) {
	if line < 1 || line > len(lines) {
		return notEditable(line, wanted)
	}
	m := flowSequence.FindStringSubmatch(lines[line-1])
	if m == nil {
		return notEditable(line, wanted)
	}
	quoted := make([]string, 0, len(add))
	for _, id := range add {
		quoted = append(quoted, yamlScalar(id))
	}
	inside := strings.Join(quoted, ", ")
	if strings.TrimSpace(m[2]) != "" {
		inside = strings.TrimRight(m[2], " ") + ", " + inside
	}
	out := append([]string(nil), lines...)
	out[line-1] = m[1] + inside + m[3]
	return joinLines(out), true, nil
}

func notEditable(line int, ids []string) (string, bool, error) {
	return "", false, fmt.Errorf("%w (around line %d): add %s to models.allow by hand",
		ErrPolicyNotEditable, line, strings.Join(ids, ", "))
}

// -----------------------------------------------------------------------------
// The block this file writes
// -----------------------------------------------------------------------------

// freshFileHeader goes only on a policy.yaml this command created. It is
// not added to a file somebody else wrote: a command run to add one model
// has no business leaving its own commentary in an operator's file.
var freshFileHeader = []string{
	"# MemQL Cockpit worker policy. models.allow is DEFAULT-DENY: only the",
	"# models listed here are offered to the cluster (docs/local-models.md).",
}

func appendModelsBlock(lines, ids []string, fresh bool) []string {
	out := append([]string(nil), lines...)
	if fresh {
		out = append(out, freshFileHeader...)
	} else if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, "models:")
	return append(out, allowBlock(2, ids)...)
}

func allowBlock(indent int, ids []string) []string {
	return append([]string{strings.Repeat(" ", indent) + "allow:"}, items(indent+2, ids)...)
}

func items(indent int, ids []string) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, pad+"- "+yamlScalar(id))
	}
	return out
}

// plainScalar is the set of ids that need no quoting. A model id is
// normally in it -- `llama3.1:8b`, `hf.co/owner/repo:Q4_K_M` -- because a
// colon only ends a key when a space follows it. Anything else is quoted
// rather than assumed safe: an id that YAML re-read as a mapping would
// make the whole file unparseable, and the worker would then serve
// nothing at all.
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/+@-]*$`)

func yamlScalar(s string) string {
	if plainScalar.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

// -----------------------------------------------------------------------------
// Node and line helpers
// -----------------------------------------------------------------------------

// documentRoot returns the single document's root mapping, or nil.
//
// A file with more than one document is refused rather than guessed at:
// which of two `models:` keys is the live one is a question this command
// has no standing to answer.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil
	}
	root := doc.Content[0]
	if isNull(root) {
		return nil
	}
	return root
}

func findKey(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

func sequenceValues(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

// insertLines puts add after the 1-based line number after.
func insertLines(lines []string, after int, add []string) []string {
	if after < 0 {
		after = 0
	}
	if after > len(lines) {
		after = len(lines)
	}
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:after]...)
	out = append(out, add...)
	return append(out, lines[after:]...)
}

// splitLines and joinLines round-trip the file. The written file always
// ends in a newline, including one that arrived without: that changes no
// key, and a YAML file whose last line has no terminator is a nuisance
// every editor silently fixes anyway.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// cleanIDs trims, drops the empties and collapses duplicates, keeping the
// caller's order -- which is the order `--model` was typed in, and the
// order the person expects to read back.
func cleanIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func missing(wanted, have []string) []string {
	present := make(map[string]struct{}, len(have))
	for _, h := range have {
		present[h] = struct{}{}
	}
	out := make([]string, 0, len(wanted))
	for _, w := range wanted {
		if _, ok := present[w]; !ok {
			out = append(out, w)
		}
	}
	return out
}

// writePolicyAtomic writes through a temp file in the SAME directory and
// renames, so a crash or a full disk leaves the old policy.yaml whole. A
// half-written one is worse than an unchanged one: the worker reads it at
// the next SIGHUP and would find a truncated allow list, which is a
// machine that silently stops serving models it was serving a minute ago.
func writePolicyAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, policyDirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".policy-*")
	if err != nil {
		return fmt.Errorf("temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, policyFileMode); err != nil {
		return fmt.Errorf("setting the mode on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming into %s: %w", path, err)
	}
	return nil
}
