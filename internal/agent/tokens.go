package agent

import (
	"encoding/json"
	"fmt"
)

// Tokens is usage in terms shared by every agent. Input counts every prompt
// token, cache reads and writes included, and Cached the cache reads among
// them; Output includes Reasoning.
type Tokens struct {
	Input     int64 `json:"input"`
	Cached    int64 `json:"cached"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
}

func (t *Tokens) Add(u Tokens) {
	t.Input += u.Input
	t.Cached += u.Cached
	t.Output += u.Output
	t.Reasoning += u.Reasoning
}

// String reads 输入 3.21M（缓存 2.10M）· 输出 120.0k（推理 80.0k）.
func (t Tokens) String() string {
	return fmt.Sprintf("输入 %s（缓存 %s）· 输出 %s（推理 %s）", count(t.Input), count(t.Cached), count(t.Output), count(t.Reasoning))
}

func count(n int64) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// TurnTokens is each turn's own usage from the records Parse stored, zero for
// a turn without one. codex reports the thread's running total and restores
// it on resume, so its turns are differences; a total that went down started
// over and is taken as is.
func TurnTokens(name string, usages []json.RawMessage) []Tokens {
	out := make([]Tokens, len(usages))
	var prev Tokens
	for i, u := range usages {
		if len(u) == 0 {
			continue
		}
		out[i] = turnTokens(name, u)
		if name == "codex" {
			cur := out[i]
			if d := (Tokens{cur.Input - prev.Input, cur.Cached - prev.Cached, cur.Output - prev.Output, cur.Reasoning - prev.Reasoning}); d.Input >= 0 && d.Cached >= 0 && d.Output >= 0 && d.Reasoning >= 0 {
				out[i] = d
			}
			prev = cur
		}
	}
	return out
}

// turnTokens maps one usage record. Only codex counts cache reads inside
// input_tokens; the others report them beside it.
func turnTokens(name string, raw json.RawMessage) Tokens {
	var u struct {
		Input         int64 `json:"input_tokens"`
		Output        int64 `json:"output_tokens"`
		CachedInput   int64 `json:"cached_input_tokens"`     // codex
		ReasoningOut  int64 `json:"reasoning_output_tokens"` // codex
		CacheRead     int64 `json:"cache_read_input_tokens"` // claude, grok
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		Reasoning     int64 `json:"reasoning_tokens"` // grok
		CacheReadAgy  int64 `json:"cache_read_tokens"`
		Thinking      int64 `json:"thinking_tokens"` // agy
		OutputDetails struct {
			Thinking int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"` // claude
		// pi, summed over a turn's messages by parsePi
		PiInput      float64 `json:"input"`
		PiOutput     float64 `json:"output"`
		PiCacheRead  float64 `json:"cacheRead"`
		PiCacheWrite float64 `json:"cacheWrite"`
		PiReasoning  float64 `json:"reasoning"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return Tokens{}
	}
	switch name {
	case "codex":
		return Tokens{u.Input, u.CachedInput, u.Output, u.ReasoningOut}
	case "claude":
		return Tokens{u.Input + u.CacheRead + u.CacheCreation, u.CacheRead, u.Output, u.OutputDetails.Thinking}
	case "grok":
		return Tokens{u.Input + u.CacheRead + u.CacheCreation, u.CacheRead, u.Output, u.Reasoning}
	case "agy":
		return Tokens{u.Input + u.CacheReadAgy, u.CacheReadAgy, u.Output, u.Thinking}
	case "pi":
		return Tokens{int64(u.PiInput + u.PiCacheRead + u.PiCacheWrite), int64(u.PiCacheRead), int64(u.PiOutput), int64(u.PiReasoning)}
	}
	return Tokens{}
}
