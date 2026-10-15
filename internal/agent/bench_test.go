package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

var benchAgents = []string{"codex", "claude", "grok", "agy", "pi"}

// streams are the real fixtures repeated to about 1 MB, the size of a long
// turn's stdout. The panel reparses a growing stdout whole on every change,
// so Events per MB is what decides whether that needs to become incremental.
func streams(b *testing.B) map[string][]byte {
	out := map[string][]byte{}
	for _, name := range benchAgents {
		one, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "stream", name+".jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		out[name] = bytes.Repeat(one, 1<<20/len(one)+1)
	}
	return out
}

func BenchmarkEvents(b *testing.B) {
	all := streams(b)
	for _, name := range benchAgents {
		stdout := all[name]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(stdout)))
			for b.Loop() {
				Events(name, stdout)
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	all := streams(b)
	for _, name := range benchAgents {
		stdout := all[name]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(stdout)))
			for b.Loop() {
				Parse(name, stdout, "", 0)
			}
		})
	}
}
