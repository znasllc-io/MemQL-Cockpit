package inference

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The macOS facts: free disk where native Ollama keeps its models, and
// deliberately NOTHING about Docker.
//
// Design D1 rules Docker out here, and this file honours that by not
// looking rather than by looking and ignoring the answer. A Docker
// Desktop found on a Mac would be a fact sitting in Host with nothing
// allowed to read it, and the next person to touch decideDarwin would
// find it and wire it up -- producing a container with no access to the
// GPU, serving on the CPU, which is exactly what the hardware floor
// exists to prevent.
func gatherPlatform(_ context.Context) platformFacts {
	return platformFacts{
		Docker: DockerFacts{
			Reason: "not probed on macOS: a container has no access to the GPU, so Docker is not a runtime option here",
		},
		FreeDisk: availableBytes(ollamaModelsDir()),
	}
}

// ollamaModelsDir is where native Ollama keeps model blobs. OLLAMA_MODELS
// overrides it, which an operator with a small internal disk and a large
// external one will have set -- and reading the home directory anyway
// would report free space on a volume the models never touch.
func ollamaModelsDir() string {
	if dir := os.Getenv("OLLAMA_MODELS"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ollama")
}

// availableBytes is what a non-root process may still write on that
// volume -- Bavail, not Bfree, which counts the reserved blocks this
// process cannot have. It is duplicated in plan_linux.go rather than
// shared: unix.Statfs_t is a different struct on each platform, so there
// is no signature that compiles on both without a fourth file.
//
// Zero means "not established". Nothing refuses on it; the pull path
// reports it beside the model's own size (design section 5).
func availableBytes(path string) uint64 {
	dir := nearestExistingDir(path)
	if dir == "" {
		return 0
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
