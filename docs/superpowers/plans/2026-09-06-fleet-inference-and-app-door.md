# Fleet Inference and the App Door (Cockpit Half) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The cockpit advertises each model's size, quantization and tool support, serves
tool-calling turns from the local runtimes, and drives Claude Code and Codex through their
own harness protocols as multi-turn sessions with a structured final answer, so the engine
can use a signed-in app as an inference door.

**Architecture:** PR 1 bumps the engine pin to the merge that carries the new wire
(`ModelCallTool`, tool calls on messages and ends, `AppDescriptor`), reads Ollama's
`details` for size and quantization, decodes `tool_calls` from both runtimes, and reports
each app's harness descriptor on `Register`. PR 2 replaces the argv runner with two harness
clients, a Codex app-server client (with the `codex mcp-server` tools as the fallback) and a
Claude Code turn-per-process client, and makes a session a sequence of turns driven by
the `message` control, ending each turn with the harness's structured result.

**Tech Stack:** Go 1.26, single module `github.com/znasllc-io/memql-cockpit`, the engine
proto reached through `.github/memql-pin` and the `replace` to `../memql`.

**Spec:** `docs/superpowers/specs/2026-09-06-fleet-inference-and-app-door-design.md`
(this repository's copy; the engine's copy is identical). Read section 2.2, 2.3, D2, D7,
D8, D11 and 4.2 first.

**Closes:** the epic issue and its task issues, filed by Task 0 in this repository.

---

## Global constraints

- **`go test ./...` IS the whole suite here** (single module, no `go.work`); `make lint`
  is fmt plus vet; the installer library has `bash scripts/install/lib_test.sh`.
- **The pin bump is ONE commit:** `.github/memql-pin` to the engine's merged sha, any new
  `require` and `replace` in `go.mod`, and `go mod tidy`. `memql-pin-guard` fails any
  workflow that checks the engine out directly; the `require` pseudo-version is not the
  pin.
- **Label grammar is mirrored by hand.** `internal/worker/models/models.go` keys and
  `String()`/`ParseAttributes` must spell `params`, `quant` and `tools` exactly as the
  engine's `integrations/agent/worker/model_routing.go` does; `TestWireContract` pins the
  copy.
- **Re-advertising costs a reconnect:** the new attributes change every machine's label
  fingerprint once; `maybeReadvertiseModels`' three guards stay.
- **A harness process never gets a live stdin from the engine** (`cmd.Stdin = nil`
  stays); a turn is one process (Claude Code) or one app-server request (Codex).
- **The MCP config is written before the app starts and deleted on every exit path**, and
  the ledger never holds the bearer; both are existing tests that must stay green.
- **Consent and the closed app set stay:** `apps.allow` and `signedIn` both true, the id
  set from `internal/worker/apps/apps.go`.
- **Commit messages explain why; gofmt and vet clean; no emojis** (this repository has no
  emoji rule of its own; the program records adopt the engine's).

---

## Task 0: file the epic and its tasks

**DONE on 2026-09-06:** epic #382; tasks #383, #384 (PR 1), #385, #386 (PR 2), in the table's order; the engine half is znasllc-io/memql#5096. Do not file them again; verify with `gh issue list --repo znasllc-io/memql-cockpit --label epic:fleet-inference-app-door`.

```bash
gh label create "epic:fleet-inference-app-door" --repo znasllc-io/memql-cockpit --color 5319e7 --force
gh issue create --repo znasllc-io/memql-cockpit --title "Epic: the cockpit half of fleet inference and the app door" \
  --label epic --label "epic:fleet-inference-app-door" --label feature --label claude --body "..."
```

Task issues (`task`, `epic:fleet-inference-app-door`, `claude`), body "Part of #<epic>.
Ships in PR n of 2. Engine epic: znasllc-io/memql#<engine epic>. Plan: docs/superpowers/plans/2026-09-06-fleet-inference-and-app-door.md":

| Title | PR |
|---|---|
| Pin bump; params, quant and tools on the model labels; tool_calls decoded from Ollama and OpenAI-compatible runtimes | 1 of 2 |
| App descriptors on Register; the Codex MCP config verified | 1 of 2 |
| The Codex app-server client, with the mcp-server tools as fallback | 2 of 2 |
| The Claude Code turn-per-process client and the turn-based session runner | 2 of 2 |

---

# PR 1: attributes, tools, descriptors

## Task 1: the pin bump and the model attributes

**Files:**
- Modify: `.github/memql-pin`, `go.mod`, `go.sum` (one commit)
- Modify: `internal/worker/models/ollama.go` (read `details.parameter_size`,
  `details.quantization_level` from `/api/tags` per model; `capabilities` contains `tools`)
- Modify: `internal/worker/models/models.go` (`Attributes.Params int64`, `.Quant string`,
  `.Tools bool`; keys; `String()`; `ParseAttributes`), `models_test.go` (`TestWireContract`
  and the round trip)
- Modify: `internal/worker/models/discover.go` (`DeclaredModel.Params`, `.Quant`, `.Tools`
  for OpenAI-compatible runtimes, declared by the operator)

**Interfaces:**
- `parseParameterSize("8.0B") (int64, bool)` turns Ollama's human string into a count
  (`B` times 1e9, `M` times 1e6; anything else absent); the label value is the integer.
- Label value example: `ctx=131072,structured=1,max=2,params=8000000000,quant=Q4_K_M,tools=1`.

- [ ] **Step 1: Tests first** — `parseParameterSize` table (`8.0B`, `70B`, `1.5B`,
  `135M`, `""`, `"unknown"`); round trip of the three keys through `String`/`Parse`; a
  `TestWireContract` update naming the engine's spelling; an `/api/tags` fixture with
  `details` yielding the attributes.
- [ ] **Step 2: The pin bump commit, then the attributes commit**

```bash
# commit 1
git add .github/memql-pin go.mod go.sum
git commit -m "pin: the engine merge that carries tools on the model call and app descriptors"
# commit 2, after go test ./... is green
git add internal/worker/models
git commit -m "models: advertise size, quantization and tool support so the engine can rank and route"
```

## Task 2: tool calls through both runtimes

**Files:**
- Modify: `internal/worker/modelcall/runtime.go` (`Message{Role, Content, ToolCallID,
  Name, ToolCalls}`, `Tool{Name, Description, ParametersJSON}`, `ChatRequest.Tools`,
  `Result.ToolCalls`, the `client` interface unchanged in shape)
- Modify: `internal/worker/modelcall/ollama.go` (`tools` on `/api/chat`; decode
  `message.tool_calls` from the NDJSON chunks), `openai.go` (`tools` on the request;
  decode `delta.tool_calls` from the SSE stream, accumulating arguments by index)
- Modify: `internal/worker/modelcall/session.go` (map `ModelCallStart.tools` and the
  message fields in, tool calls out on `ModelCallEnd` and, when the runtime streams them,
  `ModelCallDelta`; `resolve` refuses `CodeToolsUnsupported` when the model's `Tools` is
  false)
- Tests: `ollama_test.go`, `openai_test.go` with recorded fixtures of each runtime's
  tool-call shape; `session_test.go` for the refusal

- [ ] **Step 1: Fixtures and tests first** — record one real Ollama `/api/chat` tool-call
  response and one OpenAI-compatible SSE tool-call stream (from a local runtime or the
  vendors' documented shapes), assert the decoded `ToolCalls` (id, name, arguments JSON)
  and that a model without `tools` is refused before any request.
- [ ] **Step 2: Implement, run, commit**

```bash
git add internal/worker/modelcall
git commit -m "modelcall: tool calls travel both ways through Ollama and OpenAI-compatible runtimes"
```

## Task 3: app descriptors on Register, the Codex MCP config verified

**Files:**
- Modify: `internal/worker/apps/apps.go` (`Spec.Harness`, `Spec.StructuredResult`,
  `Spec.FollowUps`; Claude Code `claude-headless`, Codex `codex-app-server` when the
  binary answers `codex app-server --help`, else `codex-mcp`)
- Modify: `internal/worker/connect.go` (`Register.AppDescriptors` from the inventory)
- Modify: `internal/worker/appsession/mcpconfig.go` (`codexMCPBody` verified against a
  real Codex `config.toml` grammar; the `unverified` note replaced by the test that
  proves it)
- Tests: `apps_test.go`, `connect_test.go`, `mcpconfig_test.go`

- [ ] **Step 1: Tests first** — descriptors for both apps; a Codex binary reporting no
  app-server yields `codex-mcp`; the TOML body parses with a TOML library and yields
  `mcp_servers.memql.url` and `bearer_token` (or the field names Codex documents; the
  test reads them from a fixture copied from Codex's own docs).
- [ ] **Step 2: Implement, run, commit, push, PR 1**

```bash
git add internal/worker/apps internal/worker/connect.go internal/worker/appsession/mcpconfig.go internal/worker/appsession/mcpconfig_test.go
git commit -m "register: each app says which harness drives it; the Codex MCP config is verified"
git push -u origin epic/fleet-inference-app-door
```

Required checks are `test`, `build`, `gofmt` (ruleset `main-protection`, no merge queue,
no merge script): merge with `gh pr merge <n> --repo znasllc-io/memql-cockpit --squash`
once they are green. PR 1 closes the two PR 1 task issues.

---

# PR 2: the harnesses and the turn-based runner

## Task 4: the Codex app-server client, with the MCP tools as fallback

**Files:**
- Create: `internal/worker/harness/harness.go` (the interface), `codexappserver.go`,
  `codexmcp.go`, tests with a fake app-server over stdio
- Interfaces:

```go
// A Harness runs one app's turns for one session.
type Harness interface {
	Start(ctx context.Context, spec Spec) error                 // workspace, env, MCP config path, response schema
	Turn(ctx context.Context, prompt string, sink Sink) (TurnResult, error)
	Resume(ctx context.Context, ref string) error               // attach to the app's own session ref
	Close() error
}
type Sink interface{ Chunk(stream string, data []byte) }
type TurnResult struct { Text string; ResultJSON []byte; AppSessionRef string; Usage Usage; ExitCode int }
```

  `codexAppServer` speaks JSON-RPC 2.0 over the app-server's stdio: thread create, turn
  start, event stream to `Sink`, turn completion with the structured output when a schema
  was given, usage from the events; `codexMCP` drives `codex mcp-server`'s `codex` and
  `codex-reply` tools over stdio MCP and carries the thread id as the ref.

- [ ] **Step 1: A fake app-server first** (`harness/fake_codex_test.go`): a Go program
  on `PATH` answering the JSON-RPC methods with recorded shapes from Codex's app-server
  documentation; tests for start, a turn with events, a resume, a schema'd result, a
  process death mid-turn (error, exit code, chunks so far).
- [ ] **Step 2: Implement both clients, run, commit**

```bash
git add internal/worker/harness
git commit -m "harness: Codex through its app-server, with the mcp-server tools as the fallback"
```

## Task 5: the Claude Code client and the turn-based runner

**Files:**
- Create: `internal/worker/harness/claudeheadless.go` (+ test with a fake `claude` on
  `PATH` printing recorded stream-json events)
- Modify: `internal/worker/appsession/session.go` (`execute` chooses the harness from
  `Spec.Harness`; `Control` handles `ActionMessage` by starting the next turn; `sendEnd`
  carries `result_json` from the turn's `ResultJSON` and the ref), `chunks.go` (typed
  chunks `event`, `text`, `tool`, `stdout`, `stderr` from the harness sink), `process.go`
  unchanged (the supervisor is reused by every harness)
- Modify: `internal/worker/apps/apps.go` (`RunArgs`/`AttachArgs` deleted once the harness
  owns the argv; `InteractiveArgs` stays for the `open` kind)
- Modify: `docs/local-apps.md` (turns, results, the two harnesses, what a follow-up does)

**Interfaces:**
- Claude Code turn: `claude -p <prompt> --resume <ref> --output-format stream-json
  --verbose --mcp-config <path> --json-schema <schema>` (first turn without `--resume`);
  the `result` event yields text, `session_id` (the ref), usage; the structured output is
  the result's parsed JSON when a schema was given.

- [ ] **Step 1: Tests first** — the Claude fake with recorded events; a two-turn session
  through `Manager.Start` then `Control{message}`; the end carries the second turn's
  result; every existing `session_test.go` case still passes; the MCP config renewal now
  lands on the next turn (`TestRenewLandsOnTheNextTurn`).
- [ ] **Step 2: Implement, run, commit, push, PR 2**

```bash
git add internal/worker/harness/claudeheadless.go internal/worker/harness/claudeheadless_test.go internal/worker/appsession internal/worker/apps/apps.go docs/local-apps.md
git commit -m "app sessions: a sequence of turns through each app's own harness, with a structured result"
git push -u origin epic/fleet-inference-app-door-2
```

PR 2 closes the two PR 2 task issues and the epic; delete this plan in it. Then the engine
plan's PR 2 starts.

---

## Plan self-review

**Spec coverage.** D2: Tasks 4 and 5. D5 (cockpit side): Task 1. D7 (cockpit side):
Task 5's `message` handling and `result`. D8: Task 3. D11 (cockpit side): Task 2. Section
4.2 items 1 to 5 map to Tasks 4, 5, 5, 1 and 2, 3.

**Placeholders.** The Codex app-server method names and the TOML field names are read
from Codex's current documentation at execution time and recorded as fixtures; the plan
names the fixture files rather than guessing the shapes.

**Type consistency.** `Harness`, `Sink`, `TurnResult` are defined in Task 4 and consumed
in Task 5; `Spec.Harness` words match the engine plan's `AppDescriptor.Harness` set;
`Attributes` keys match the engine's `ModelAttributes` keys by the wire-contract test.
