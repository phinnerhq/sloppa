package httpapi

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
)

func TestAgentsAccess(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	token := h.machineToken()

	// Editing takes a person: a leaked machine token mustn't reach every machine's agents.
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/agents"},
		{"PUT", "/api/agents/instructions"},
		{"PUT", "/api/agents/skills/review"},
		{"PATCH", "/api/agents/skills/review"},
		{"DELETE", "/api/agents/skills/review"},
		{"POST", "/api/agents/skills/update"},
		{"POST", "/api/agents/sources"},
	} {
		res := h.send(h.req(r.method, r.path, map[string]any{}).bearer(token))
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a machine token: %d %s", r.method, r.path, res.Code, res.Body)
		}
	}
	// Syncing takes a machine: a browser has no machine to plan for.
	res := h.send(h.req("GET", "/api/agents/sync", nil).cookie(session))
	if code, _ := errorCode(t, res); res.Code != http.StatusUnauthorized || code != "unauthorized" {
		t.Errorf("sync plan with a session: %d %s", res.Code, res.Body)
	}
	if res := h.do("GET", "/api/agents/skills/review/files", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("skill files signed out: %d", res.Code)
	}
}

func TestAgentsSyncRoundTrip(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	token := h.machineToken()

	res := h.send(h.req("PUT", "/api/agents/instructions", map[string]string{"content": "Be nice.\n"}).cookie(session))
	if res.Code != http.StatusNoContent {
		t.Fatalf("set instructions: %d %s", res.Code, res.Body)
	}
	body := `{"files":[{"path":"SKILL.md","content":"` + base64.StdEncoding.EncodeToString([]byte("---\nname: review\ndescription: Reviews.\n---\n")) + `"}]}`
	res = h.send(h.req("PUT", "/api/agents/skills/review", strings.NewReader(body)).header("Content-Type", "application/json").cookie(session))
	if res.Code != http.StatusOK {
		t.Fatalf("put skill: %d %s", res.Code, res.Body)
	}
	skill := decode[api.Skill](t, res)
	if skill.Description != "Reviews." {
		t.Errorf("skill = %+v", skill)
	}

	plan := decode[api.SyncPlan](t, h.send(h.req("GET", "/api/agents/sync", nil).bearer(token)))
	if plan.Instructions != "Be nice.\n" || len(plan.Skills) != 1 || plan.Skills[0].Hash != skill.Hash {
		t.Errorf("plan = %+v", plan)
	}
	content := decode[api.SkillContent](t, h.send(h.req("GET", "/api/agents/skills/review/files", nil).bearer(token)))
	if len(content.Files) != 1 || api.SkillHash(content.Files) != skill.Hash {
		t.Errorf("content = %+v", content)
	}

	res = h.send(h.req("POST", "/api/agents/sync", map[string]any{"fingerprint": plan.Fingerprint, "problems": []string{}, "cliVersion": "test"}).bearer(token))
	if res.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", res.Code, res.Body)
	}
	o := decode[api.AgentsOverview](t, h.send(h.req("GET", "/api/agents", nil).cookie(session)))
	if len(o.Machines) != 1 || o.Machines[0].LastSync == nil || o.Machines[0].LastSync.Fingerprint != o.Machines[0].Fingerprint {
		t.Errorf("overview = %+v", o)
	}

	res = h.send(h.req("GET", "/api/agents/skills/nope/files", nil).bearer(token))
	if code, _ := errorCode(t, res); res.Code != http.StatusNotFound || code != "skill_not_found" {
		t.Errorf("unknown skill: %d %s", res.Code, res.Body)
	}
	res = h.send(h.req("PUT", "/api/agents/instructions/nope", map[string]string{"content": "x"}).cookie(session))
	if code, _ := errorCode(t, res); res.Code != http.StatusNotFound || code != "machine_not_found" {
		t.Errorf("unknown machine: %d %s", res.Code, res.Body)
	}
}

// A skill near the size limit fits in a request, base64 and all.
func TestAgentsTakeLargeSkills(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	big := strings.Repeat("x", api.SkillBytesMax-100)
	body := `{"files":[{"path":"SKILL.md","content":"` + base64.StdEncoding.EncodeToString([]byte(big)) + `"}]}`
	res := h.send(h.req("PUT", "/api/agents/skills/big", strings.NewReader(body)).header("Content-Type", "application/json").cookie(session))
	if res.Code != http.StatusOK {
		t.Fatalf("put a large skill: %d %.300s", res.Code, res.Body)
	}
}
