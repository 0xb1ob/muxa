package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func isShellCommand(cmd string) bool {
	base := filepath.Base(cmd)
	switch base {
	case "zsh", "bash", "fish", "sh", "dash", "ksh":
		return true
	}
	return false
}

func paneStatus(kind, cwd, cmd string) string {
	if cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return "ghost"
		}
	}
	switch kind {
	case "claude", "cursor", "pi":
		if isShellCommand(cmd) {
			return "ghost"
		}
	}
	return "live"
}

func paneWhoState(kind, cwd, cmd, pane string, drawing []string) string {
	if paneStatus(kind, cwd, cmd) == "ghost" {
		return "ghost"
	}
	for _, d := range drawing {
		if d == pane {
			return "busy"
		}
	}
	return "idle"
}

func commandLineForPID(pid string) string {
	if pid == "" {
		return ""
	}
	if b, err := os.ReadFile("/proc/" + pid + "/cmdline"); err == nil && len(b) > 0 {
		s := strings.ReplaceAll(string(b), "\x00", " ")
		return strings.TrimSpace(s)
	}
	out, err := exec.Command("ps", "-p", pid, "-o", "args=").Output()
	if err != nil {
		out, err = exec.Command("ps", "-p", pid, "-o", "command=").Output()
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(out))
}

func paneChildPIDs(parent string) []string {
	if parent == "" {
		return nil
	}
	out, err := exec.Command("ps", "-ax", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	var kids []string
	for _, line := range strings.Split(string(out), "\n") {
		fs := strings.Fields(line)
		if len(fs) < 2 {
			continue
		}
		if fs[1] == parent {
			kids = append(kids, fs[0])
		}
	}
	return kids
}

func kindFromPIDTree(pid string) string {
	if pid == "" {
		return "generic"
	}
	if args := commandLineForPID(pid); args != "" {
		if k := kindFromArgv(args); k != "generic" {
			return k
		}
	}
	for _, child := range paneChildPIDs(pid) {
		args := commandLineForPID(child)
		if args == "" {
			continue
		}
		if k := kindFromArgv(args); k != "generic" {
			return k
		}
	}
	return "generic"
}

func kindFromCommand(cmd string) string {
	return kindFromArgv(cmd)
}

// kindFromArgv classifies from a full process command line. Match executable
// basenames and anchored path segments — not bare substrings like "omp" in
// compose or "claude" in claude-projects.
func kindFromArgv(line string) string {
	if strings.Contains(line, "cursor-agent") {
		return "cursor"
	}
	tok, rest, _ := strings.Cut(line, " ")
	base := filepath.Base(tok)
	switch {
	case strings.HasPrefix(base, "claude"):
		return "claude"
	case base == "agent" || base == "cursor-agent":
		return "cursor"
	case base == "omp" || base == "pi":
		return "pi"
	}
	rest = strings.TrimLeft(rest, " \t")
	switch {
	case strings.Contains(rest, "cursor-agent"):
		return "cursor"
	case strings.Contains(rest, "/claude/") || strings.Contains(rest, "/claude-code") || strings.Contains(rest, "/claude-code/"):
		return "claude"
	}
	switch {
	case strings.Contains(line, "/agent ") || strings.Contains(line, " agent ") || strings.HasSuffix(line, " agent") || strings.HasPrefix(line, "agent "):
		return "cursor"
	}
	return "generic"
}

func detectKind(t *TMUX, pane string) string {
	cmd, _ := t.fmt(pane, "#{pane_current_command}")
	base := filepath.Base(cmd)
	switch {
	case strings.HasPrefix(base, "claude"):
		return "claude"
	case base == "agent" || base == "cursor-agent":
		return "cursor"
	case base == "omp" || base == "pi":
		return "pi"
	}
	pid, _ := t.fmt(pane, "#{pane_pid}")
	k := kindFromPIDTree(pid)
	if k == "" {
		return "generic"
	}
	return k
}

func resolveHookKind(t *TMUX, pane, hookKind string) string {
	detected := detectKind(t, pane)
	if detected != "" && detected != "generic" {
		return detected
	}
	if hookKind != "" {
		return hookKind
	}
	existing, _ := t.fmt(pane, "#{@muxa_kind}")
	if existing != "" {
		return existing
	}
	return "generic"
}
