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
func (a *Agent) skillRoute(task router.TaskReq, calls []provider.ToolCall) router.TaskReq {
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
		if m.Tier != "" {
			task.Tier = m.Tier
		}
		if m.Context != "" {
			task.Context = m.Context
		}
		if m.Privacy != "" {
			task.Privacy = m.Privacy
		}
		task.Vision = task.Vision || m.Vision
	}
	return task
}
