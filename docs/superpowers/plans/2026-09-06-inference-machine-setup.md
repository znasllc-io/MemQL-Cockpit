# Inference Machine Setup (Cockpit Half) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `memql worker setup --inference` turns a person's machine into an inference
machine in one command: floor check, runtime present or installed (Docker on Linux, native
Ollama on Apple Silicon), models pulled from the Ollama library or Hugging Face with
progress, `models.allow` written, the worker re-advertising; and a machine can be told to
pull a model from MemQL OS over a new message pair.

**Architecture:** PR 1 adds an `internal/worker/inference` package (plan, install, pull,
allow, re-advertise) driven by the CLI and reusing the app-session process supervisor for
the pull, reads Ollama's `details` for size and quantization, and teaches the installers a
`--inference` flag. PR 2 handles `ModelPullStart` from the engine, streams progress, and
re-advertises immediately after a successful pull.

**Tech Stack:** Go 1.26, single module; the installers' shell library; the engine proto
through the pin.

**Spec:** `docs/superpowers/specs/2026-09-06-inference-machine-setup-design.md` (this
repository's copy). Read D1 to D5 and section 4.1 first. Depends on epic 3's cockpit PR 1
for the `params`/`quant` label keys (do not add them twice: if epic 3 has not merged, this
plan's Task 2 lands them and epic 3's Task 1 becomes a no-op for those keys).

**Closes:** the epic issue and its task issues, filed by Task 0.

---

## Global constraints

- **`go test ./...` is the whole suite**; `make lint`; `bash scripts/install/lib_test.sh`.
- **Never sudo, never `curl | sh`.** The runtime install runs the commands it prints and
  asked about; on Linux the Docker route (`docker run` with GPU passthrough), on macOS
  Homebrew (`brew install ollama`) or "open the app", and `--non-interactive` refuses an
  install with exit 3.
- **Docker on macOS is not offered** (no GPU in a container); Linux requires the NVIDIA
  container toolkit (or ROCm) for the Docker route and refuses without it, naming the
  package.
- **`policy.yaml` is written atomically with mode 0600** and every key outside
  `models.allow` survives byte-for-byte; `models.allow` merges, never replaces.
- **A pull runs under the app-session supervisor** (`internal/worker/appsession/process.go`:
  own process group, partial-line flush, SIGTERM then SIGKILL), never through
  `workerHost.exec` (600 s cap, buffered output).
- **Re-advertising costs a reconnect**; a remote pull re-advertises AT ONCE, once,
  bypassing the two-minute guard for that case only.
- **The pin bump is one commit** (`.github/memql-pin`, `go.mod` require/replace, `go mod tidy`).
- **Commit messages explain why; gofmt and vet clean; no emojis.**

---

## Task 0: file the epic and its tasks

**DONE on 2026-09-06:** epic #387; tasks #388, #389 (PR 1), #390 (PR 2); the engine half is znasllc-io/memql#5103. Do not file them again; verify with `gh issue list --repo znasllc-io/memql-cockpit --label epic:inference-machine-setup`.

Label `epic:inference-machine-setup`; the epic issue (`epic`, the label, `feature`,
`claude`) links the engine epic filed by the engine plan's Task 0. Task issues (`task`,
the label, `claude`):

| Title | PR |
|---|---|
| `inference.Plan`: floor, platform, runtime and Docker detection, with the sentences it prints | 1 of 2 |
| `setup --inference`: runtime install, model pull with progress, `models.allow`, re-advertise; installers' `--inference` | 1 of 2 |
| `ModelPull*` handled from the engine: streamed progress, allow on success, immediate re-advertise | 2 of 2 |

---

# PR 1: the local flow

## Task 1: `inference.Plan`

**Files:**
- Create: `internal/worker/inference/plan.go`, `plan_test.go`, `plan_darwin.go`,
  `plan_linux.go`, `plan_other.go`

**Interfaces:**

```go
type Host struct {
	GOOS, GOARCH string
	Floor        models.FloorVerdict         // from internal/worker/models
	Ollama       models.Inventory            // Probe result, may be empty
	Docker       DockerFacts                 // Present, Version, GPUToolkit (linux), Reason
	FreeDisk     uint64                      // bytes on the runtime's volume
	LookPath     func(string) (string, error)
}
type Runtime string // "native", "docker"
type Plan struct {
	Runtime        Runtime
	RuntimePresent bool
	Install        []string   // the commands that would run, empty when present
	Refusal        string     // non-empty when the machine cannot be an inference machine; the sentence to print
	DefaultModels  []string   // llama3.1:8b, nomic-embed-text
}
func Gather(ctx context.Context, d *models.Discoverer) (Host, error)
func Decide(h Host) Plan
```

`Decide` rules: floor unmet means `Refusal` with the floor's own `Detail`; darwin means
`Runtime = native`, install `brew install ollama` when `LookPath("ollama")` fails (with
the sentence "Docker is not offered on macOS: a container has no access to the GPU");
linux means `Runtime = docker`, install the `docker run -d --name ollama --gpus all
--restart unless-stopped -v ollama:/root/.ollama -p 127.0.0.1:11434:11434 ollama/ollama`
line when no Ollama answers, refused with the toolkit's package name when Docker is
present and `GPUToolkit` is false, refused naming Docker when Docker is absent; other
platforms refused.

- [ ] **Step 1: Tests first** — a table over fixture hosts: Apple Silicon with Ollama,
  Apple Silicon without (brew line), Linux with Docker and toolkit (docker line), Linux
  with Docker and no toolkit (refusal names `nvidia-container-toolkit`), Linux without
  Docker (refusal names Docker), below floor (refusal is the floor's detail), unsupported
  platform. Each case asserts the exact sentence.
- [ ] **Step 2: Implement, run, commit**

```bash
git add internal/worker/inference
git commit -m "inference: decide what a machine needs to serve models, and say it in one sentence"
```

## Task 2: `setup --inference`, the pull, the allow-list, the installers

**Files:**
- Create: `internal/worker/inference/install.go`, `pull.go`, `allow.go`, `readvertise.go`
  and tests
- Modify: `internal/worker/cli.go` (`setup --inference [--model <id>]... [--non-interactive]
  [--runtime docker|native]`, `models --pull <id>`, `models --allow <id>`), `models_cmd.go`
  (the "Install Ollama" sentence becomes "run `memql worker setup --inference`")
- Modify: `internal/worker/models/ollama.go` (`details.parameter_size`,
  `details.quantization_level` read from `/api/tags`; skipped if epic 3 landed them)
- Modify: `scripts/install/install-mac.sh`, `install-linux.sh`, `lib.sh`, `lib_test.sh`
  (`--inference` pass-through: after `worker.yaml` is written, run `memql worker setup
  --inference --non-interactive` and, when it refuses with exit 3 because an install is
  needed, print the interactive command for the person to run)
- Modify: `docs/local-models.md`, `README.md`

**Interfaces:**

```go
func InstallRuntime(ctx context.Context, p Plan, consent func(commands []string) bool, run Runner) error
type Progress struct { Model string; Status string; Completed, Total uint64 }
func Pull(ctx context.Context, ollamaBase, model string, onProgress func(Progress)) error   // `ollama pull <model>` under the supervisor; parses the progress lines
func Allow(policyPath string, ids ...string) error                                          // atomic, 0600, merge
func Readvertise(ctx context.Context) error                                                 // SIGHUP the running worker, or no-op with a sentence
```

Hugging Face: `Pull` accepts `hf.co/<owner>/<repo>` and `hf.co/<owner>/<repo>:<quant>`
unchanged; Ollama resolves them.

- [ ] **Step 1: Tests first** — `Pull` over a fake `ollama` on `PATH` that prints a
  progress bar without newlines then exits 0 (progress callbacks observed, order
  preserved), then one exiting 1 (error carries the last line); `Allow` on an existing
  `policy.yaml` with other keys (byte-identical outside `models.allow`, mode 0600,
  duplicates merged); `InstallRuntime` with a refusing consent runs nothing;
  `--non-interactive` exits 3 without installing; `lib_test.sh` covers `--inference` on
  both installers.
- [ ] **Step 2: Implement, run, commit, push, PR 1**

```bash
git add internal/worker/inference internal/worker/cli.go internal/worker/models_cmd.go internal/worker/models/ollama.go scripts/install docs/local-models.md README.md
git commit -m "setup --inference: one command from a bare machine to a model the cluster can see"
git push -u origin epic/inference-machine-setup
```

Merge with `gh pr merge <n> --repo znasllc-io/memql-cockpit --squash` once `test`, `build`
and `gofmt` are green. Closes the two PR 1 task issues.

---

# PR 2: driven from the OS

## Task 3: `ModelPull*` from the engine

**Files:**
- Modify: `.github/memql-pin`, `go.mod`, `go.sum` (one commit, to the engine merge that
  carries `ModelPullStart/Progress/End`)
- Modify: `internal/worker/loop.go` (a `ModelPullStart` arm beside `AppSessionStart`; a
  one-shot immediate re-advertise after a successful pull that bypasses
  `modelReadvertiseMinInterval` once), `internal/worker/inference/pull.go` (progress to
  the wire), `internal/worker/tools/policy.go` (`models.pull: true|false`, default true)
- Tests: `loop_test.go` with a fake sender; a refused pull when `models.pull` is false

- [ ] **Step 1: Tests first** — a start yields progress messages then an end with `ok`;
  a failed pull ends with `ok=false` and the runtime's last line; policy off refuses
  before any process; the re-advertise happens once within the guard window.
- [ ] **Step 2: Pin bump commit, then the handler commit, push, PR 2**

```bash
git add .github/memql-pin go.mod go.sum
git commit -m "pin: the engine merge that carries the model pull messages"
git add internal/worker/loop.go internal/worker/loop_test.go internal/worker/inference/pull.go internal/worker/tools/policy.go
git commit -m "model pull: the engine can ask a machine to pull, watch it, and see the model at once"
git push -u origin epic/inference-machine-setup-2
```

Closes the PR 2 task issue and the epic; delete this plan in it.

---

## Plan self-review

**Spec coverage.** D1: Task 1. D2: Task 2. D3: Task 3. D4: Task 2's `/api/tags` read (or
epic 3's). D5: the engine plan. Section 4.1 items map to Tasks 1, 2, 2, 3, 2, 2. Section 6
tests are named in each task.

**Placeholders.** The exact Docker GPU flag set and the toolkit package names are read
from the vendors' current docs at execution time and pinned in the test table's
sentences; the plan states the shape.

**Type consistency.** `Plan`, `Host`, `Progress` are defined in Tasks 1 and 2 and
consumed by Task 3; the `params`/`quant` keys are the epic 3 spelling.
