package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Agent setup kept on the server and synced to every machine by `sloppa agents sync`: instructions
// (shared, then each machine's own) written to ~/.agents/AGENTS.md, and skills, each a folder with a
// SKILL.md, written to ~/.agents/skills/<name>.

const (
	// InstructionsMax bounds the shared instructions and each machine's, in bytes.
	InstructionsMax = 256 << 10
	// SkillBytesMax and SkillFilesMax bound one skill's files.
	SkillBytesMax = 8 << 20
	SkillFilesMax = 500
	SkillNameMax  = 64
)

var skillName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)

// IsSkillName tells whether name works as a skill's folder on every machine: lowercase letters,
// digits, dots, dashes and underscores, at most 64, starting and ending with a letter or digit.
func IsSkillName(name string) bool { return skillName.MatchString(name) }

var sourceName = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

// IsSkillSource tells whether source names a GitHub repository, as owner/repo.
func IsSkillSource(source string) bool {
	return sourceName.MatchString(source) && !strings.HasSuffix(source, "/.") && !strings.HasSuffix(source, "/..")
}

// ComposeInstructions is what a machine's ~/.agents/AGENTS.md holds: the shared instructions, then
// a blank line and the machine's own, when it has any.
func ComposeInstructions(shared, machine string) string {
	shared = strings.TrimRight(shared, "\n")
	machine = strings.TrimSpace(machine)
	switch {
	case machine == "" && shared == "":
		return ""
	case machine == "":
		return shared + "\n"
	case shared == "":
		return machine + "\n"
	}
	return shared + "\n\n" + machine + "\n"
}

// SkillFile is one file of a skill. Content goes as base64 in JSON.
type SkillFile struct {
	// Path is relative to the skill's folder, with forward slashes, like `scripts/run.sh`.
	Path       string `json:"path"`
	Executable bool   `json:"executable,omitempty"`
	Content    []byte `json:"content"`
}

// Skill is one skill the machines get, as the dashboard lists it.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Source is the GitHub repository the skill was imported from, as owner/repo; absent for skills
	// uploaded by hand.
	Source string `json:"source,omitempty"`
	// SourcePath is the skill's folder in Source, "" for its root.
	SourcePath string `json:"sourcePath,omitempty"`
	// SourceRef is the commit of Source the files came from.
	SourceRef string `json:"sourceRef,omitempty"`
	// Hash is a SHA-256 of the files, their paths and whether they're executable: the skill's version.
	Hash      string    `json:"hash"`
	Files     int       `json:"files"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Enabled skills go to every machine but those in DisabledOn; disabled ones to none.
	Enabled    bool     `json:"enabled"`
	DisabledOn []string `json:"disabledOn"`
}

// SkillContent is a skill's files, for a machine to write them or the dashboard to show them.
type SkillContent struct {
	Name  string      `json:"name"`
	Hash  string      `json:"hash"`
	Files []SkillFile `json:"files"`
}

// MachineAgents is a machine as the agents page shows it: what it should have, and what it said it
// had the last time it synced.
type MachineAgents struct {
	MachineID string `json:"machineId"`
	Name      string `json:"name"`
	// Instructions are the machine's own, appended to the shared ones.
	Instructions string `json:"instructions"`
	// Fingerprint is a hash of everything the machine should have: it's in sync when its last sync
	// reported the same.
	Fingerprint string      `json:"fingerprint"`
	LastSync    *SyncReport `json:"lastSync,omitempty"`
}

// AgentsOverview is everything the agents page shows.
type AgentsOverview struct {
	Instructions string          `json:"instructions"`
	Skills       []Skill         `json:"skills"`
	Machines     []MachineAgents `json:"machines"`
}

// SkillRef is a skill a machine should have, at a version.
type SkillRef struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// SyncPlan is what one machine should have, for `sloppa agents sync`.
type SyncPlan struct {
	Fingerprint string `json:"fingerprint"`
	// Instructions are composed already: the shared ones, then the machine's.
	Instructions string     `json:"instructions"`
	Skills       []SkillRef `json:"skills"`
}

// SyncReport is what `sloppa agents sync` says it did, and when the server heard it.
type SyncReport struct {
	// Fingerprint is the plan's that was applied, whether or not everything went right.
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
	// Problems are what couldn't be done, for people: none when the machine has everything.
	Problems   []string `json:"problems"`
	CLIVersion string   `json:"cliVersion,omitempty"`
}

// SourceSkill is a skill found in a GitHub repository, which may be imported.
type SourceSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Path is its folder in the repository, "" for the root.
	Path string `json:"path"`
	// Imported tells whether a skill by that name is among the account's already.
	Imported bool `json:"imported"`
}

// SourceListing is the skills a repository has, at the commit read.
type SourceListing struct {
	Source string        `json:"source"`
	Ref    string        `json:"ref"`
	Skills []SourceSkill `json:"skills"`
}

// SkillUpdates is how updating skills from their sources went.
type SkillUpdates struct {
	// Updated changed; Unchanged were up to date already.
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`
	// Failed are the sources that couldn't be read, or no longer have a skill, with why.
	Failed []SkillUpdateFailure `json:"failed"`
}

type SkillUpdateFailure struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// SkillHash is a skill's version: a SHA-256 over its files, their paths and whether they're executable.
// Files must be sorted by path, as the server keeps them.
func SkillHash(files []SkillFile) string {
	h := sha256.New()
	for _, f := range files {
		exec := byte('0')
		if f.Executable {
			exec = '1'
		}
		fmt.Fprintf(h, "%s\x00%c\x00%d\x00", f.Path, exec, len(f.Content))
		h.Write(f.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}
