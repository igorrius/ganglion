package agent

import (
	"encoding/json"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/tool/builtin"
)

// Skills are the loaded skills (skills.Set).
type Skills interface {
	Index() string
	Get(name string) (skills.Skill, bool)
}

// skillsHeader opens the skills index in an admin's system prompt.
const skillsHeader = "Skills (read one with skill.read before you follow it):\n"

// skillRoute returns task with the model block of a skill the model just
// read: the rest of the turn runs on what the skill asks for (R-RTR-4).
// Only an admin's turn is re-routed (skill.read is theirs alone), and
// local-only, once set, stays for the turn: reading another skill cannot
// send it to the cloud. The task stays a turn (TaskReq.Turn), so the daily
// budget still cuts it whatever purpose the skill asks for.
func (a *Agent) skillRoute(task router.TaskReq, calls []provider.ToolCall, admin bool) router.TaskReq {
	if !admin {
		return task
	}
	for _, c := range calls {
		if c.Name != builtin.SkillReadTool {
			continue
		}
		var arg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(c.Args, &arg) != nil {
			continue
		}
		sk, ok := a.d.Skills.Get(arg.Name)
		if !ok {
			continue
		}
		m := sk.Model
		if m.Purpose != "" {
			task.Purpose = m.Purpose
		}
		if m.Context != "" {
			task.Context = m.Context
		}
		if m.Privacy != "" && task.Privacy != skills.PrivacyLocalOnly {
			task.Privacy = m.Privacy
		}
		task.Vision = task.Vision || m.Vision
	}
	return task
}
