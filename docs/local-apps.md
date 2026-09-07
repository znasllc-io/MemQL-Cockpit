# Local apps as execution surfaces — the machine side

The cockpit can let a planner Task run inside an app the machine's owner
**already pays for** — Claude Code or Codex — on their own computer, with
MemQL's tools reachable from inside that app over MCP.

The canonical record is the engine's
[`docs/public/operate/local-apps.md`](https://github.com/znasllc-io/memql/blob/main/docs/public/operate/local-apps.md)
(epic [memql#4358](https://github.com/znasllc-io/memql/issues/4358)). This page
is the half that happens on the machine: what to configure, what gets written
where, and what to check when it does not work.

---

## Turning it on

Nothing is on by default. Two switches, and both must be thrown:

**1. `apps.allow` in `~/.memql/policy.yaml`** — the machine owner's word.

```yaml
apps:
  allow:
    - claude-code
    - codex
```

`SIGHUP` the worker (or restart it) and the change takes effect on the next
heartbeat. An empty or absent list means **nothing is allowed** — an app
session does exactly what `workerHost.exec` does, so it gets the same
default-deny posture as the rest of `policy.yaml`.

**2. Sign in to the app itself.** The engine routes to a machine only when the
app is both **allowed** and **signed in**, so a machine with the binary but no
session is never selected.

Delegation also has to be enabled per USER, in the portal at `/machines`. That
half is not configured here.

---

## What the cockpit reports

On `Register` and on **every** heartbeat:

| Field | Where it comes from |
|---|---|
| `id` | `claude-code` / `codex` |
| `version` | the CLI's own `--version` output, verbatim |
| `signed_in` | the app's own state files (see below) |
| `subscription` | what the app REPORTS; `unknown` when it said nothing |
| `allowed` | `policy.yaml apps.allow` |

An app that is **present but not allowed** is still reported, with
`allowed=false`. The portal shows it as present-and-blocked, which is
something an operator can act on; omitting it would look exactly like "not
installed".

### How `signed_in` is detected, and why it errs toward `false`

- **Claude Code** — `~/.claude/.credentials.json` (the no-keyring shape), or
  the account record in `~/.claude.json`.
- **Codex** — `~/.codex/auth.json`, an API key or an OAuth token pair.

Nothing here shells out to a real prompt to answer a status question, and
nothing reads the OS keyring: the worker runs as a LaunchAgent on macOS, and a
keyring read there raises a Keychain prompt on a machine whose owner may not be
sitting at it.

**Consequence, stated plainly:** on a macOS box where Claude Code put its token
in the Keychain and wrote no account record, this reports `signed_in=false`.
That is the designed direction of the error. The machine is simply not
selected, and `/machines` says "not signed in" — rather than showing a green
row for a machine that will refuse the run after a plan has already committed
to it.

The version is cached (keyed on the binary's size and mtime, so an in-place
upgrade invalidates it immediately); presence and auth state are read fresh on
every beat, so signing in shows up on the **next beat** rather than the next
reconnect.

---

## What a session writes on the machine

| Path | What it is | Lifetime |
|---|---|---|
| `<workspace>/.mcp.json` | Claude Code's project-scoped MCP config, `0600`, carrying the per-run bearer | deleted at session end |
| `<workspace>/.memql-session/codex/config.toml` | Codex's MCP config in a per-session `CODEX_HOME` | deleted at session end |
| `<workspace>/.memql-session/transcript.log` | the full transcript | pushed to the Library, then deleted |
| `<workspace>/.memql-session/open-launcher.sh` | the `open` kind's terminal launcher | deleted at session end |
| `<state_dir>/appsessions/<id>.json` | the write ledger — **paths only, never the bearer** | deleted at session end |

A pre-existing `.mcp.json` in the workspace is moved aside and **restored** at
the end, so an `open` or `attach` session pointed at a real project does not
destroy a config somebody wrote.

### The deletion is the security control

The per-run bearer **cannot be revoked**. The engine's verify path is
JWKS-only and DB-free — that is what lets it work on every node without a
lookup — so there is no row to strike, and revoking one token would mean
rotating the cluster's signing key. Three things stand in for revocation: an
8-hour hard cap at the identity service, renewal in place so no single bearer
is long-lived, and **this file being deleted**.

A `defer` cannot cover a SIGKILL, an OOM, or a machine that lost power, so
every write is recorded in the ledger and the worker **sweeps** it at startup.
A non-zero count in that log line at boot means a previous process died with a
live session:

```
level=WARN msg="swept MCP configuration files left by a previous cockpit process" files=1
```

**Known limitation** (tracked on
[memql-cockpit#348](https://github.com/znasllc-io/memql-cockpit/issues/348)):
Claude Code reads its MCP configuration at **startup**. A `renew_credential`
mid-run rewrites the file correctly, but the already-running process will not
pick it up — so a run that outlives its bearer loses MemQL's tools even though
the file on disk is current. The engine can shorten the run instead; that is
its decision to make, and it can only make it because this says so.

---

## Turns, and the harness that drives them

A session is a **sequence of turns**, not one process with its output read off
the terminal. The cockpit drives each app through the app's **own protocol**,
so what the app said, what it spent, and which session to continue are three
facts rather than three guesses.

| Harness | What it is | Structured result | Usage |
|---|---|---|---|
| `claude-headless` | `claude -p --output-format stream-json --verbose`, **one process per turn**, continued with `--resume <session id>` | yes, via `--json-schema` | yes, from the `result` event |
| `codex-app-server` | `codex app-server`, JSON-RPC 2.0 over the child's stdio, one process per session and one request per turn | yes, via `outputSchema` | yes, per turn |
| `codex-mcp` | `codex mcp-server`, the `codex` / `codex-reply` tool pair over stdio MCP | **no** | **no** |

**Which Codex you have decides which of the two you get**, and only this
machine can tell: `codex app-server --help` is the probe, and a Codex too old
to answer it falls back to `codex-mcp`. The answer is cached against the
binary's size and mtime, so an upgrade takes effect at once. The session runner
and the inventory ask the same question the same way, so what a session drives
and what the machine advertises cannot disagree — the descriptor reaches
`Register` once the engine has a field for it
([memql#5096](https://github.com/znasllc-io/memql/issues/5096)), and the engine
then never asks for a protocol this machine cannot speak.

**Codex through `codex mcp-server` reports no usage and cannot constrain the
answer, and both are stated rather than papered over.** The tool pair's output
schema is `{threadId, content}` and nothing else; the token counts on its event
stream are described by Codex itself as "accumulated, estimated, or replayed",
and an estimate written to a ledger somebody bills from is worse than a gap. It
also takes no output-schema argument, and its input schema refuses arguments it
does not declare — so a turn that asked for a schema runs **unconstrained** and
reports a structured result only if the answer happens to parse.

### A follow-up

`AppSessionControl{action: "message"}` starts the **next turn in the same
conversation**. It is delivered even when it arrives while a turn is running:
the prompt is queued and the loop takes it as soon as the turn ends. Turns are
strictly sequential — two processes resuming one Claude Code session id would
both append to the same transcript and neither would see the other's turn — and
a follow-up that cannot be delivered (the session is finishing, or eight are
already waiting) is **refused out loud**, in the log and in the transcript,
rather than dropped.

The session ends when a turn finishes and nothing is queued behind it.

The control's `action` is a plain string, so `message` needs nothing new on the
wire. Its **prompt** does: until memql#5096 adds `prompt` to
`AppSessionControl`, the follow-up's text travels in `reason`.

Because Claude Code reads its MCP configuration at startup and each turn is a
new process, a `renew_credential` in the middle of a session **does** reach the
next turn. That is the honest form of what renewal could never do for one long
process.

### A structured result

When the engine asks for an answer against a JSON Schema and the app produces
one, it leaves as the session's **last chunk**: an `event` chunk whose body is
`{"type": "memql.app_session.result", "session_id": …, "result": …}`. The type
word is namespaced because that same stream carries the app's own events, and
Claude Code's own last line is literally `{"type":"result", …}`.

**There is often no structured result, and that is not a failure.** No schema
was asked for; or the app answered in prose; or the app is a Codex driven
through `codex-mcp`, which cannot be constrained at all. The run is reported as
having succeeded, with a note in the transcript, because "the app ran and
answered unstructured" bills and retries differently from "the app never
started". A result is never synthesised: an empty object would read downstream
as *the app answered nothing*.

Today **no session asks for a schema**: `AppSessionStart` has no field to carry
one until memql#5096, so every turn runs unconstrained and no result chunk is
produced. The machine side is in place and waiting for the field.

### Chunk streams

| Stream | What it carries |
|---|---|
| `event` | structured progress, as the app reported it |
| `text` | assistant prose meant for a person |
| `tool` | a tool call or its result |
| `stdout` | anything printed that the protocol did not account for — a stack trace on the way down |
| `stderr` | the process's stderr, verbatim |

`text` and `tool` are new with the harnesses. An engine that predates them
keeps every chunk regardless and renders the two as narration, so the finer
split is what is lost, never the words.

---

## The three kinds

- **`run`** — headless and autonomous. This is the only kind anything
  initiates today.
- **`open`** — launches the app for the human in a terminal window, with the
  workspace and prompt loaded. On macOS it resolves a terminal application
  (iTerm, Ghostty, Alacritty, kitty, WezTerm, Terminal); on Linux it uses
  `$TERMINAL` or the first of `x-terminal-emulator`, `gnome-terminal`,
  `konsole`, `xfce4-terminal`, `alacritty`, `kitty`, `wezterm`, `foot`,
  `xterm`. Anywhere else it **fails immediately with a reason** rather than
  doing nothing.
- **`attach`** — resumes the app's own session named by `app_session_ref` and
  sends it a turn. It needs a prompt: neither app offers a way to *watch* a run
  somebody else started, so an attach with nothing to say would spend the
  machine owner's subscription asking the app nothing. It is refused by name
  instead.

An `open` that cannot launch ends the session **at once** with a non-empty
error. It never falls back to a headless run: the user asked to drive it
themselves.

---

## Outputs

Files the run created or changed in the workspace are pushed to the Library,
plus the full transcript. Bounded, and the bounds are announced rather than
applied silently:

- 64 MiB per file, 32 files per session — anything dropped is named in the log
  **and** in a chunk, so the transcript says what is missing;
- `.git`, `node_modules`, `vendor`, `target`, `dist`, `build`, `.venv`,
  `__pycache__` and friends are never pushed — reproducible, enormous, and
  they would bury the actual output;
- nested paths are flattened (`api/schema.json` → `api__schema.json`) because
  the Library keys on one path segment, and two `schema.json` files from
  different directories would otherwise arrive indistinguishable.

---

## When it does not work

| What you see | What it means |
|---|---|
| `/machines` shows the app but not selectable | one of `allowed` / `signed in` is false; the badge says which |
| The machine never appears at all | `claude` / `codex` is not on the worker's `PATH`. A LaunchAgent's `PATH` is not your shell's |
| `is not in this machine's policy.yaml apps.allow` | the engine routed here anyway; add it to `apps.allow` or ask why the label was derived |
| `is allowed here but is not on this worker's PATH` | the binary moved, or the worker's `PATH` is not your shell's. A LaunchAgent inherits neither your shell profile nor a version manager's shims |
| `kind=attach ... needs a prompt` | an attach that only wanted to watch. Send the turn you want run, or use `run` |
| `this session is no longer taking turns` | a `message` control arrived after the last turn had already ended the session |
| `the app answered, but not against the schema` | the run SUCCEEDED and produced no structured result. For `codex-mcp` that is expected: it cannot constrain the answer at all |
| `not found (404) -- either the artifact does not exist, or the owning user cannot reach it` | the Library answers 404 for both on purpose, so a link cannot probe which ids exist. Check the OWNING USER's access, not the worker token |
| `the session credential was rejected (401)` | this one IS the cockpit's side: the bearer expired or is malformed |
| `no display: DISPLAY and WAYLAND_DISPLAY are both unset` | an `open` on a headless box. Correct refusal |
| `live transcript truncated at limits.max_transcript_bytes` | the engine's row cap bit. The complete transcript is the pushed artifact |

## Related

- [computer-use.md](computer-use.md) — the other surface that touches the
  machine directly
- The engine's
  [workers runbook](https://github.com/znasllc-io/memql/blob/main/docs/public/operate/workers-runbook.md)
