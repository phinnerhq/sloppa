package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
)

// problemsMax and problemMax bound what a sync report keeps.
const (
	problemsMax = 50
	problemMax  = 1000
)

// Service is the agent setup in the state database.
type Service struct {
	db     *sql.DB
	github *GitHub
}

// New is the agent setup in db, a state database already migrated, importing from github.
func New(db *sql.DB, github *GitHub) *Service { return &Service{db: db, github: github} }

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func now() int64 { return time.Now().UnixMilli() }

// sharedInstructions are a's, "" before any were saved.
func sharedInstructions(ctx context.Context, q querier, userID int64) (string, error) {
	var content string
	err := q.QueryRowContext(ctx, `SELECT content FROM agent_instructions WHERE user_id = ?`, userID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the instructions: %w", err)
	}
	return content, nil
}

const skillColumns = `name, description, source, source_path, source_ref, hash, files, size, enabled, updated_ms`

func scanSkill(s interface{ Scan(...any) error }) (api.Skill, error) {
	var (
		k                              api.Skill
		description, src, srcPath, ref sql.NullString
		updated                        int64
	)
	err := s.Scan(&k.Name, &description, &src, &srcPath, &ref, &k.Hash, &k.Files, &k.Size, &k.Enabled, &updated)
	k.Description, k.Source, k.SourcePath, k.SourceRef = description.String, src.String, srcPath.String, ref.String
	k.UpdatedAt = api.Millis(updated)
	k.DisabledOn = []string{}
	return k, err
}

// skills are userID's skills by name, each with the machines it's off on.
func skills(ctx context.Context, q querier, userID int64) ([]api.Skill, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+skillColumns+` FROM skills WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	all := []api.Skill{}
	for rows.Next() {
		k, err := scanSkill(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("list skills: %w", err)
		}
		all = append(all, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT skill, machine_id FROM skill_exclusions WHERE user_id = ? ORDER BY machine_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list skill exclusions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var skill, machine string
		if err := rows.Scan(&skill, &machine); err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(all, func(k api.Skill) bool { return k.Name == skill }); i >= 0 {
			all[i].DisabledOn = append(all[i].DisabledOn, machine)
		}
	}
	return all, rows.Err()
}

func findSkill(ctx context.Context, q querier, userID int64, name string) (api.Skill, error) {
	all, err := skills(ctx, q, userID)
	if err != nil {
		return api.Skill{}, err
	}
	i := slices.IndexFunc(all, func(k api.Skill) bool { return k.Name == name })
	if i < 0 {
		return api.Skill{}, api.SkillNotFound(name)
	}
	return all[i], nil
}

// machine is one of an account's machines, with its own instructions.
type machine struct {
	id, name, instructions string
}

// machines are userID's, newest first, as the dashboard lists them.
func machines(ctx context.Context, q querier, userID int64) ([]machine, error) {
	rows, err := q.QueryContext(ctx, `SELECT machines.id, machines.name, COALESCE(machine_instructions.content, '')
		FROM machines LEFT JOIN machine_instructions ON machine_instructions.machine_id = machines.id
		WHERE machines.user_id = ? ORDER BY machines.created_ms DESC, machines.rowid DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
	}
	defer rows.Close()
	var all []machine
	for rows.Next() {
		var m machine
		if err := rows.Scan(&m.id, &m.name, &m.instructions); err != nil {
			return nil, err
		}
		all = append(all, m)
	}
	return all, rows.Err()
}

// ownMachine is an api.MachineNotFound unless id is one of userID's machines.
func ownMachine(ctx context.Context, q querier, userID int64, id string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM machines WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return api.MachineNotFound(id)
	}
	return err
}

// plan is what machine should have of shared and all.
func plan(shared string, m machine, all []api.Skill) api.SyncPlan {
	p := api.SyncPlan{Instructions: api.ComposeInstructions(shared, m.instructions), Skills: []api.SkillRef{}}
	for _, k := range all {
		if k.Enabled && !slices.Contains(k.DisabledOn, m.id) {
			p.Skills = append(p.Skills, api.SkillRef{Name: k.Name, Hash: k.Hash})
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "instructions\x00%d\x00%s", len(p.Instructions), p.Instructions)
	for _, k := range p.Skills {
		fmt.Fprintf(h, "\x00skill\x00%s\x00%s", k.Name, k.Hash)
	}
	p.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return p
}

// lastSyncs are what each of userID's machines said when it last synced.
func lastSyncs(ctx context.Context, q querier, userID int64) (map[string]api.SyncReport, error) {
	rows, err := q.QueryContext(ctx, `SELECT agent_syncs.machine_id, agent_syncs.fingerprint, agent_syncs.problems, agent_syncs.cli_version, agent_syncs.synced_ms
		FROM agent_syncs JOIN machines ON machines.id = agent_syncs.machine_id WHERE machines.user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("list syncs: %w", err)
	}
	defer rows.Close()
	reports := map[string]api.SyncReport{}
	for rows.Next() {
		var (
			id, problems string
			r            api.SyncReport
			version      sql.NullString
			at           int64
		)
		if err := rows.Scan(&id, &r.Fingerprint, &problems, &version, &at); err != nil {
			return nil, err
		}
		r.Problems = []string{}
		_ = json.Unmarshal([]byte(problems), &r.Problems)
		r.CLIVersion, r.At = version.String, api.Millis(at)
		reports[id] = r
	}
	return reports, rows.Err()
}

// Overview is everything the agents page shows.
func (s *Service) Overview(ctx context.Context, a accounts.Account) (api.AgentsOverview, error) {
	shared, err := sharedInstructions(ctx, s.db, a.ID)
	if err != nil {
		return api.AgentsOverview{}, err
	}
	all, err := skills(ctx, s.db, a.ID)
	if err != nil {
		return api.AgentsOverview{}, err
	}
	ms, err := machines(ctx, s.db, a.ID)
	if err != nil {
		return api.AgentsOverview{}, err
	}
	reports, err := lastSyncs(ctx, s.db, a.ID)
	if err != nil {
		return api.AgentsOverview{}, err
	}
	o := api.AgentsOverview{Instructions: shared, Skills: all, Machines: []api.MachineAgents{}}
	for _, m := range ms {
		ma := api.MachineAgents{MachineID: m.id, Name: m.name, Instructions: m.instructions, Fingerprint: plan(shared, m, all).Fingerprint}
		if r, ok := reports[m.id]; ok {
			ma.LastSync = &r
		}
		o.Machines = append(o.Machines, ma)
	}
	return o, nil
}

func checkInstructions(content string) error {
	if len(content) > api.InstructionsMax {
		return api.PayloadTooLarge(fmt.Sprintf("Instructions are at most %d KiB", api.InstructionsMax>>10))
	}
	return nil
}

// SetInstructions replaces the instructions every machine gets.
func (s *Service) SetInstructions(ctx context.Context, a accounts.Account, content string) error {
	if err := checkInstructions(content); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_instructions (user_id, content, updated_ms) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET content = excluded.content, updated_ms = excluded.updated_ms`, a.ID, content, now())
	if err != nil {
		return fmt.Errorf("save the instructions: %w", err)
	}
	return nil
}

// SetMachineInstructions replaces the instructions only machineID gets, after the shared ones.
func (s *Service) SetMachineInstructions(ctx context.Context, a accounts.Account, machineID, content string) error {
	if err := checkInstructions(content); err != nil {
		return err
	}
	if err := ownMachine(ctx, s.db, a.ID, machineID); err != nil {
		return err
	}
	var err error
	if strings.TrimSpace(content) == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM machine_instructions WHERE machine_id = ?`, machineID)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO machine_instructions (machine_id, content, updated_ms) VALUES (?, ?, ?)
			ON CONFLICT (machine_id) DO UPDATE SET content = excluded.content, updated_ms = excluded.updated_ms`, machineID, content, now())
	}
	if err != nil {
		return fmt.Errorf("save the machine's instructions: %w", err)
	}
	return nil
}

// origin is where a skill's files came from: a repository's folder at a commit, or nowhere for an
// upload.
type origin struct{ source, path, ref string }

// save writes a skill's files under name, keeping whether it's on and where. changed is false when it
// had these files already, which leaves it as it was, origin included.
func save(ctx context.Context, db *sql.DB, userID int64, name string, files []api.SkillFile, from origin) (k api.Skill, changed bool, err error) {
	hash := api.SkillHash(files)
	_, description := Frontmatter(skillMD(files))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return api.Skill{}, false, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT hash FROM skills WHERE user_id = ? AND name = ?`, userID, name).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return api.Skill{}, false, err
	}
	changed = current != hash
	if changed {
		_, err = tx.ExecContext(ctx, `INSERT INTO skills (user_id, name, description, source, source_path, source_ref, hash, files, size, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, name) DO UPDATE SET description = excluded.description, source = excluded.source,
				source_path = excluded.source_path, source_ref = excluded.source_ref, hash = excluded.hash,
				files = excluded.files, size = excluded.size, updated_ms = excluded.updated_ms`,
			userID, name, nullable(description), nullable(from.source), nullable(from.path), nullable(from.ref), hash,
			len(files), totalSize(files), now())
		if err != nil {
			return api.Skill{}, false, fmt.Errorf("save %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM skill_files WHERE user_id = ? AND skill = ?`, userID, name); err != nil {
			return api.Skill{}, false, err
		}
		for _, f := range files {
			_, err := tx.ExecContext(ctx, `INSERT INTO skill_files (user_id, skill, path, executable, content) VALUES (?, ?, ?, ?, ?)`,
				userID, name, f.Path, f.Executable, f.Content)
			if err != nil {
				return api.Skill{}, false, fmt.Errorf("save %s's files: %w", name, err)
			}
		}
	} else if from.source != "" {
		// The same files, from a newer commit maybe: the skill is up to date as of it.
		_, err = tx.ExecContext(ctx, `UPDATE skills SET source = ?, source_path = ?, source_ref = ? WHERE user_id = ? AND name = ?`,
			from.source, nullable(from.path), nullable(from.ref), userID, name)
		if err != nil {
			return api.Skill{}, false, err
		}
	}
	k, err = findSkill(ctx, tx, userID, name)
	if err != nil {
		return api.Skill{}, false, err
	}
	return k, changed, tx.Commit()
}

// PutSkill saves files as the skill name, uploaded by hand: one imported before is no longer updated
// from its source.
func (s *Service) PutSkill(ctx context.Context, a accounts.Account, name string, files []api.SkillFile) (api.Skill, error) {
	if !api.IsSkillName(name) {
		return api.Skill{}, api.BadRequest(fmt.Sprintf("%q isn't a skill name: lowercase letters, digits, dots, dashes and underscores, at most %d", name, api.SkillNameMax))
	}
	files, err := checkFiles(files)
	if err != nil {
		return api.Skill{}, err
	}
	k, changed, err := save(ctx, s.db, a.ID, name, files, origin{})
	if err != nil {
		return api.Skill{}, err
	}
	if !changed && k.Source != "" {
		// The same files, but by hand now.
		if _, err := s.db.ExecContext(ctx, `UPDATE skills SET source = NULL, source_path = NULL, source_ref = NULL WHERE user_id = ? AND name = ?`, a.ID, name); err != nil {
			return api.Skill{}, err
		}
		k.Source, k.SourcePath, k.SourceRef = "", "", ""
	}
	return k, nil
}

func checkSource(source string) error {
	if !api.IsSkillSource(source) {
		return api.BadRequest(fmt.Sprintf("%q isn't a GitHub repository: expected owner/repo", source))
	}
	return nil
}

// ListSource is the skills in source, a GitHub repository, and which of them a has already.
func (s *Service) ListSource(ctx context.Context, a accounts.Account, source string) (api.SourceListing, error) {
	source = NormalizeSource(source)
	if err := checkSource(source); err != nil {
		return api.SourceListing{}, err
	}
	repo, err := s.github.fetch(ctx, source, nil)
	if err != nil {
		return api.SourceListing{}, err
	}
	have, err := skills(ctx, s.db, a.ID)
	if err != nil {
		return api.SourceListing{}, err
	}
	l := api.SourceListing{Source: source, Ref: repo.ref, Skills: []api.SourceSkill{}}
	for _, k := range repo.skills {
		imported := slices.ContainsFunc(have, func(h api.Skill) bool { return h.Name == k.name })
		l.Skills = append(l.Skills, api.SourceSkill{Name: k.name, Description: k.description, Path: k.path, Imported: imported})
	}
	return l, nil
}

// ImportSkills saves names from source, a GitHub repository, as it is now: new skills, or ones a had
// already, which then follow source.
func (s *Service) ImportSkills(ctx context.Context, a accounts.Account, source string, names []string) ([]api.Skill, error) {
	source = NormalizeSource(source)
	if err := checkSource(source); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, api.BadRequest("Name the skills to import")
	}
	repo, err := s.github.fetch(ctx, source, func(k repoSkill) bool { return slices.Contains(names, k.name) })
	if err != nil {
		return nil, err
	}
	var picked []repoSkill
	for _, name := range names {
		k, ok := repo.skill(name)
		if !ok {
			return nil, api.SourceUnavailable(fmt.Sprintf("%s has no skill %s", source, name))
		}
		if k.err != nil {
			return nil, k.err
		}
		picked = append(picked, k)
	}
	imported := []api.Skill{}
	for _, k := range picked {
		saved, _, err := save(ctx, s.db, a.ID, k.name, k.files, origin{source: source, path: k.path, ref: repo.ref})
		if err != nil {
			return nil, err
		}
		imported = append(imported, saved)
	}
	return imported, nil
}

// UpdateSkills brings names (every imported skill, when none) up to date with their sources, reading
// each source once.
func (s *Service) UpdateSkills(ctx context.Context, a accounts.Account, names []string) (api.SkillUpdates, error) {
	all, err := skills(ctx, s.db, a.ID)
	if err != nil {
		return api.SkillUpdates{}, err
	}
	for _, name := range names {
		if !slices.ContainsFunc(all, func(k api.Skill) bool { return k.Name == name }) {
			return api.SkillUpdates{}, api.SkillNotFound(name)
		}
	}
	bySource := map[string][]api.Skill{}
	for _, k := range all {
		if k.Source != "" && (len(names) == 0 || slices.Contains(names, k.Name)) {
			bySource[k.Source] = append(bySource[k.Source], k)
		}
	}
	u := api.SkillUpdates{Updated: []string{}, Unchanged: []string{}, Failed: []api.SkillUpdateFailure{}}
	for _, source := range slices.Sorted(maps.Keys(bySource)) {
		// Found by folder, so a skill renamed upstream still follows; by name otherwise.
		repo, err := s.github.fetch(ctx, source, func(r repoSkill) bool {
			return slices.ContainsFunc(bySource[source], func(k api.Skill) bool { return k.SourcePath == r.path || k.Name == r.name })
		})
		for _, k := range bySource[source] {
			if err != nil {
				u.Failed = append(u.Failed, api.SkillUpdateFailure{Name: k.Name, Message: err.Error()})
				continue
			}
			// Found by its folder, so a skill renamed upstream still follows; by name otherwise.
			found, ok := repoSkill{}, false
			for _, r := range repo.skills {
				if r.path == k.SourcePath {
					found, ok = r, true
				}
			}
			if !ok {
				found, ok = repo.skill(k.Name)
			}
			switch {
			case !ok:
				u.Failed = append(u.Failed, api.SkillUpdateFailure{Name: k.Name, Message: fmt.Sprintf("%s no longer has this skill", source)})
				continue
			case found.err != nil:
				u.Failed = append(u.Failed, api.SkillUpdateFailure{Name: k.Name, Message: found.err.Error()})
				continue
			}
			_, changed, err := save(ctx, s.db, a.ID, k.Name, found.files, origin{source: source, path: found.path, ref: repo.ref})
			if err != nil {
				return api.SkillUpdates{}, err
			}
			if changed {
				u.Updated = append(u.Updated, k.Name)
			} else {
				u.Unchanged = append(u.Unchanged, k.Name)
			}
		}
	}
	return u, nil
}

// ConfigureSkill turns name on or off everywhere, and replaces the machines it stays off on.
func (s *Service) ConfigureSkill(ctx context.Context, a accounts.Account, name string, enabled *bool, disabledOn []string) (api.Skill, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Skill{}, err
	}
	defer tx.Rollback()
	if _, err := findSkill(ctx, tx, a.ID, name); err != nil {
		return api.Skill{}, err
	}
	if enabled != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE skills SET enabled = ? WHERE user_id = ? AND name = ?`, *enabled, a.ID, name); err != nil {
			return api.Skill{}, err
		}
	}
	if disabledOn != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM skill_exclusions WHERE user_id = ? AND skill = ?`, a.ID, name); err != nil {
			return api.Skill{}, err
		}
		for _, id := range slices.Compact(slices.Sorted(slices.Values(disabledOn))) {
			if err := ownMachine(ctx, tx, a.ID, id); err != nil {
				return api.Skill{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO skill_exclusions (user_id, skill, machine_id) VALUES (?, ?, ?)`, a.ID, name, id); err != nil {
				return api.Skill{}, err
			}
		}
	}
	k, err := findSkill(ctx, tx, a.ID, name)
	if err != nil {
		return api.Skill{}, err
	}
	return k, tx.Commit()
}

// DeleteSkill drops name: machines remove it the next time they sync.
func (s *Service) DeleteSkill(ctx context.Context, a accounts.Account, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE user_id = ? AND name = ?`, a.ID, name)
	if err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return api.SkillNotFound(name)
	}
	return nil
}

// SkillContent is name's files.
func (s *Service) SkillContent(ctx context.Context, a accounts.Account, name string) (api.SkillContent, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM skills WHERE user_id = ? AND name = ?`, a.ID, name).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return api.SkillContent{}, api.SkillNotFound(name)
	}
	if err != nil {
		return api.SkillContent{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path, executable, content FROM skill_files WHERE user_id = ? AND skill = ? ORDER BY path`, a.ID, name)
	if err != nil {
		return api.SkillContent{}, fmt.Errorf("read %s's files: %w", name, err)
	}
	defer rows.Close()
	c := api.SkillContent{Name: name, Hash: hash, Files: []api.SkillFile{}}
	for rows.Next() {
		var f api.SkillFile
		if err := rows.Scan(&f.Path, &f.Executable, &f.Content); err != nil {
			return api.SkillContent{}, err
		}
		c.Files = append(c.Files, f)
	}
	return c, rows.Err()
}

// Plan is what the machine a signed in as should have.
func (s *Service) Plan(ctx context.Context, a accounts.Account) (api.SyncPlan, error) {
	shared, err := sharedInstructions(ctx, s.db, a.ID)
	if err != nil {
		return api.SyncPlan{}, err
	}
	all, err := skills(ctx, s.db, a.ID)
	if err != nil {
		return api.SyncPlan{}, err
	}
	m := machine{id: a.MachineID}
	err = s.db.QueryRowContext(ctx, `SELECT content FROM machine_instructions WHERE machine_id = ?`, a.MachineID).Scan(&m.instructions)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return api.SyncPlan{}, err
	}
	return plan(shared, m, all), nil
}

// Report keeps what the machine a signed in as said its sync did.
func (s *Service) Report(ctx context.Context, a accounts.Account, fingerprint string, problems []string, cliVersion string) error {
	if len(problems) > problemsMax {
		problems = append(problems[:problemsMax-1], fmt.Sprintf("…and %d more", len(problems)-problemsMax+1))
	}
	for i, p := range problems {
		if len(p) > problemMax {
			problems[i] = p[:problemMax] + "…"
		}
	}
	if problems == nil {
		problems = []string{}
	}
	encoded, err := json.Marshal(problems)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_syncs (machine_id, fingerprint, problems, cli_version, synced_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (machine_id) DO UPDATE SET fingerprint = excluded.fingerprint, problems = excluded.problems,
			cli_version = excluded.cli_version, synced_ms = excluded.synced_ms`,
		a.MachineID, fingerprint, string(encoded), nullable(cliVersion), now())
	if err != nil {
		return fmt.Errorf("save the sync report: %w", err)
	}
	return nil
}

// NormalizeSource reads a GitHub URL, like https://github.com/owner/repo.git, as owner/repo.
func NormalizeSource(source string) string {
	source = strings.TrimSpace(source)
	for _, prefix := range []string{"https://", "http://"} {
		source = strings.TrimPrefix(source, prefix)
	}
	source = strings.TrimPrefix(source, "www.")
	source = strings.TrimPrefix(source, "github.com/")
	source = strings.TrimSuffix(strings.TrimSuffix(source, "/"), ".git")
	// Only owner/repo of a deeper link, like …/tree/main/skills.
	if parts := strings.Split(source, "/"); len(parts) > 2 {
		source = parts[0] + "/" + parts[1]
	}
	return source
}
