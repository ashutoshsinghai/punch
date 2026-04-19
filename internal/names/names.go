// Package names generates random human-friendly display names for chat sessions.
// Names are adjective-noun pairs (e.g. "swift-river", "cosmic-panda") —
// fresh every session, no configuration required.
package names

import (
	"fmt"
	"math/rand"
)

var adjectives = []string{
	"swift", "cosmic", "silent", "golden", "fierce", "bright", "dark", "quiet",
	"wild", "calm", "bold", "sharp", "deep", "frost", "lunar", "ember", "azure",
	"crimson", "silver", "iron", "thunder", "crystal", "amber", "copper", "jade",
	"misty", "electric", "hollow", "ancient", "neon", "hidden", "rapid", "velvet",
	"phantom", "blazing", "frozen", "solar", "orbital", "quantum", "noble",
}

var nouns = []string{
	"river", "panda", "tiger", "hawk", "wolf", "fox", "storm", "moon", "star",
	"peak", "ridge", "brook", "tide", "flame", "spark", "echo", "pulse", "comet",
	"nebula", "phoenix", "cipher", "drifter", "beacon", "nomad", "ranger", "ghost",
	"signal", "vector", "prism", "quasar", "raven", "falcon", "lynx", "ibis",
	"mantis", "condor", "bison", "otter", "walrus", "coyote",
}

// Random returns a random adjective-noun name such as "swift-river".
func Random() string {
	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	return fmt.Sprintf("%s-%s", adj, noun)
}
