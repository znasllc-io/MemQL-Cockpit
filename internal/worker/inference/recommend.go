package inference

// The recommended model set, by machine class.
//
// Fixed by the engine's open-weight-defaults design record (D2), whose
// ids were verified against the Ollama library on 2026-09-07. The
// engine computes the same answer in recommendedSet(class, hardware,
// catalog) and that is the AUTHORITY -- this copy exists because
// `setup --inference` has to choose a set before any engine round trip,
// on a machine that may not be paired at all. When the engine names a
// set for this machine, the engine's set wins; this is the answer for a
// machine nobody has told anything.
//
// WHY THIS IS A FUNCTION AND NOT A PAIR. The August default
// (llama3.1:8b, nomic-embed-text) was a floor chosen when one pair had
// to serve every machine, and it is now wrong in both directions at
// once: it leaves two thirds of a 64 GB machine idle, and it predates
// the generation of open-weight models that carry tools, thinking,
// structured output and vision in a single record -- which is the whole
// reason ranking a fleet on `params` means anything.
//
// A CLASS BELOW THE SMALLEST STILL GETS A SET, and that is deliberate.
// The class gates the RECOMMENDATION, never the machine: the hardware
// floor already decided whether this machine serves at all, and a Linux
// box with 8 GB of VRAM clears that floor and holds a 9B model
// comfortably while classing `unsupported`. Handing it an empty set
// would be this command refusing a machine that works.

// The default pair, named as constants because the acceptance criterion
// for this change is that they are asserted BY NAME -- a test comparing
// two expressions that both read defaultGeneral would pass on any pair.
const (
	// DefaultGeneralModel is the 16 GB workhorse: tools, thinking,
	// structured output and vision in one record.
	DefaultGeneralModel = "qwen3.5:9b"
	// DefaultEmbeddingModel is the small embedder. The operations this
	// fleet serves locally are both kinds -- planning and conductor
	// turns, and embeddings -- so a machine that pulled only the first
	// would advertise a set that silently cannot answer half of them.
	DefaultEmbeddingModel = "qwen3-embedding:0.6b"

	// The larger models, added by class.
	strongModel   = "qwen3.8:27b"
	largestModel  = "qwen3.5:35b"
	mixtureModel  = "gemma4:26b"
	largeEmbedder = "qwen3-embedding:4b"
)

// RecommendedSet is the ordered list `setup --inference` pulls for a
// machine of this class.
//
// THE ORDER IS THE PULL ORDER AND IT IS NOT INCIDENTAL. Two things
// depend on it: the closing block reads the machine back in the order
// the ids were asked for, so an operator comparing the two does not
// have to hunt; and the embedder goes LAST on every class, because a
// run interrupted halfway should leave a machine with a general model
// rather than with only an embedder -- the second is a machine that
// answers no prompts at all while looking configured.
func RecommendedSet(class string) []string {
	switch class {
	case "64", "128":
		return []string{DefaultGeneralModel, strongModel, largestModel, mixtureModel, largeEmbedder}
	case "32":
		return []string{DefaultGeneralModel, strongModel, DefaultEmbeddingModel}
	default:
		// 16, 24, unsupported, and anything unrecognised or empty.
		//
		// An UNKNOWN class takes the smallest set rather than none, and
		// the asymmetry is the point: guessing small costs a slower
		// model on a machine that could have run a bigger one, and
		// guessing large fills somebody's disk with a model their GPU
		// cannot hold. The first is a disappointment and the second is
		// a support ticket.
		return []string{DefaultGeneralModel, DefaultEmbeddingModel}
	}
}
