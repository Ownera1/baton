package app

import "baton/internal/agent"

// Stat totals the finished tasks run by one agent or role with one model and
// effort, to compare them by time, tokens and cost.
type Stat struct {
	Agent     string `json:"agent"`
	Role      string `json:"role,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Tasks     int    `json:"tasks"`
	Succeeded int    `json:"succeeded"`
	FirstTurn int    `json:"first_turn"` // succeeded with a single turn
	Turns     int    `json:"turns"`
	// AgentMS is turn time outside checks: the agent, plus Baton's worktree,
	// commit and patch. Time queued for a slot is not counted.
	AgentMS int64        `json:"agent_ms"`
	CheckMS int64        `json:"check_ms"`
	Tokens  agent.Tokens `json:"tokens"`
	// CostUSD sums the costs agents reported; codex and agy report none.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// Stats totals the finished tasks of the repository containing repo (every
// repository when ""), only those in group when it is set. Groups come in the
// order their agent was last used.
func (s *Service) Stats(repo, group string) ([]*Stat, error) {
	if repo != "" {
		root, err := repoRoot(repo)
		if err != nil {
			return nil, err
		}
		repo = root
	}
	list, err := s.List(group)
	if err != nil {
		return nil, err
	}
	out := []*Stat{}
	byKey := map[[4]string]*Stat{}
	for _, t := range list.Tasks {
		if t.Unreadable || (repo != "" && t.Repo != repo) || t.Status == "queued" || t.Status == "running" || t.Status == "stale" {
			continue
		}
		key := [4]string{t.Agent, t.Role, t.Model, t.Effort}
		st := byKey[key]
		if st == nil {
			st = &Stat{Agent: t.Agent, Role: t.Role, Model: t.Model, Effort: t.Effort}
			byKey[key] = st
			out = append(out, st)
		}
		st.Tasks++
		st.Turns += len(t.Turns)
		if t.Status == "succeeded" {
			st.Succeeded++
			if len(t.Turns) == 1 {
				st.FirstTurn++
			}
		}
		for _, turn := range t.Turns {
			if turn.Tokens != nil {
				st.Tokens.Add(*turn.Tokens)
			}
			if turn.CostUSD != nil {
				if st.CostUSD == nil {
					st.CostUSD = new(float64)
				}
				*st.CostUSD += *turn.CostUSD
			}
			var check int64
			if turn.Verify != nil {
				check = turn.Verify.DurationMS
			}
			st.CheckMS += check
			if turn.StartedAt != nil && turn.EndedAt != nil {
				st.AgentMS += turn.EndedAt.Sub(*turn.StartedAt).Milliseconds() - check
			}
		}
	}
	return out, nil
}
