package domain

// KnowledgeDef configures the knowledge index of a project (the
// knowledge: block of barnraise.yaml).
type KnowledgeDef struct {
	// Symbols is how symbols outside Go are found: "auto" (ast-grep when
	// it is installed, the native extractor otherwise) or "native".
	Symbols string     `json:"symbols"`
	Map     RepoMapDef `json:"map"`
}

// RepoMapDef configures the repository map (.barnraise/map.md): one line
// per file, written by Barnraise when an item is integrated, and a
// relevant part of it given to every session.
type RepoMapDef struct {
	Enabled      bool `json:"enabled"`
	MaxFiles     int  `json:"max_files"`     // lines of the file; the rest is summarized per folder
	PromptTokens int  `json:"prompt_tokens"` // share of the context pack; 0 leaves the map out of prompts
}

// RepoMapFile is where the repository map lives, relative to the root.
const RepoMapFile = ".barnraise/map.md"
