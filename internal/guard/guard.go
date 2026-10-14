// Copied from collaborative-work (same author).
// Package guard keeps the resource cost of automated testing inside the budget
// of the machine it runs on. The defaults are set for a 16 GB Apple Silicon
// laptop: a single check at a time, a small slice of CPU, a hard memory ceiling
// and a refusal to start when the machine is already busy.
//
// Nothing here is a security sandbox. It constrains the processes this tool
// starts, and it reports honestly when a constraint stopped a check from
// finishing.
package guard

import (
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCPUs        = 2
	DefaultNice        = 10
	DefaultMemoryMB    = 3072
	DefaultSampleEvery = time.Second
)

type Limits struct {
	CPUs        int           `json:"cpus"`
	Nice        int           `json:"nice"`
	MemoryMB    int           `json:"memory_mb"`
	SampleEvery time.Duration `json:"-"`
}

// withDefaults returns a copy with every unset field filled in.
func (l Limits) withDefaults() Limits {
	if l.CPUs <= 0 {
		l.CPUs = DefaultCPUs
	}
	if l.Nice < 0 {
		l.Nice = 0
	}
	if l.MemoryMB <= 0 {
		l.MemoryMB = DefaultMemoryMB
	}
	if l.SampleEvery <= 0 {
		l.SampleEvery = DefaultSampleEvery
	}
	return l
}

// CheckEnv is injected into every check so build systems do not fan out across
// all cores by default. These only bound parallelism; none of them changes what
// a check computes.
func (l Limits) CheckEnv() []string {
	l = l.withDefaults()
	cpus := strconv.Itoa(l.CPUs)
	env := []string{
		"GOMAXPROCS=" + cpus,
		"GOFLAGS=-p=" + cpus,
		"MAKEFLAGS=-j" + cpus,
		"CMAKE_BUILD_PARALLEL_LEVEL=" + cpus,
		"CARGO_BUILD_JOBS=" + cpus,
		"RAYON_NUM_THREADS=" + cpus,
		"OMP_NUM_THREADS=" + cpus,
		"NUMEXPR_NUM_THREADS=" + cpus,
		"VECLIB_MAXIMUM_THREADS=" + cpus,
		"NPM_CONFIG_MAXSOCKETS=" + cpus,
	}
	return env
}

// ProcessGroupRSS distinguishes a process that has exited from a monitor error.
func ProcessGroupRSS(pgid int) (int64, error) {
	if pgid <= 0 {
		return 0, nil
	}
	out, err := runPS("-eo", "pgid=,rss=")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		g, err := strconv.Atoi(fields[0])
		if err != nil || g != pgid {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		total += kb
	}
	return total, nil
}

// ProcessTreeRSS includes children that started their own session, as native
// sandbox launchers do. Group-only accounting misses those processes.
func ProcessTreeRSS(root int) (int64, error) {
	if root <= 0 {
		return 0, nil
	}
	out, err := runPS("-eo", "pid=,ppid=,pgid=,rss=")
	if err != nil {
		return 0, err
	}
	return treeRSS(out, root), nil
}

func treeRSS(table string, root int) int64 {
	type proc struct {
		pid, parent, group int
		rss                int64
	}
	rows := []proc{}
	selected := map[int]bool{root: true}
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		parent, e2 := strconv.Atoi(f[1])
		group, e3 := strconv.Atoi(f[2])
		rss, e4 := strconv.ParseInt(f[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		rows = append(rows, proc{pid, parent, group, rss})
		if group == root {
			selected[pid] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, row := range rows {
			if !selected[row.pid] && selected[row.parent] {
				selected[row.pid] = true
				changed = true
			}
		}
	}
	var total int64
	for _, row := range rows {
		if selected[row.pid] {
			total += row.rss
		}
	}
	return total
}
