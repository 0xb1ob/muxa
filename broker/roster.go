package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// tmux <=3.4 rewrites literal tab and other control bytes in -F output to
// "_", which breaks delimited parsing on Ubuntu CI. "||" survives unmodified
// and will not appear in these fields in practice (same as the bash port).
const rosterSep = "||"

type rosterEntry struct {
	Pane, Name, ID, Parent, Kind, Where, Cwd, Cmd string
}

type whoJSONRow struct {
	Name    string  `json:"name"`
	ID      string  `json:"id"`
	Parent  *string `json:"parent"`
	Kind    string  `json:"kind"`
	State   string  `json:"state"`
	Pane    string  `json:"pane"`
	Session *string `json:"session"`
	Cwd     string  `json:"cwd"`
}

var nameAdjectives = []string{
	"amber", "azure", "bold", "brave", "bright", "calm", "crisp", "eager", "fair",
	"gentle", "golden", "keen", "lively", "lucid", "lunar", "merry", "noble",
	"proud", "quiet", "rustic", "silent", "silver", "solar", "sturdy", "swift", "vivid",
}

var nameNouns = []string{
	"badger", "brook", "canyon", "cedar", "comet", "ember", "falcon", "fern", "fox",
	"glacier", "grove", "harbor", "hawk", "heron", "lark", "maple", "meadow", "oak",
	"otter", "pebble", "pine", "raven", "ridge", "river", "sparrow", "stone", "willow", "wolf",
}

var shlexSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shlexSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shlexJoin(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = shlexQuote(p)
	}
	return strings.Join(quoted, " ")
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func newMuxaID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%012x", mrand.Int63n(1<<48))
	}
	return hex.EncodeToString(buf[:])
}

func loadRoster(t *TMUX) ([]rosterEntry, error) {
	format := strings.Join([]string{
		"#{pane_id}", "#{@muxa_name}", "#{@muxa_id}", "#{@muxa_parent}",
		"#{@muxa_kind}", "#{session_name}:#{window_index}.#{pane_index}",
		"#{pane_current_path}", "#{pane_current_command}",
	}, rosterSep)
	out, err := t.Run([]string{"list-panes", "-a", "-F", format}, nil)
	if err != nil {
		return nil, err
	}
	return parseRosterLines(out), nil
}

func isMuxaPane(name, id, parent, kind string) bool {
	if name != "" {
		return true
	}
	return id != "" || parent != "" || kind != ""
}

func rosterPendingName(pane, id string) string {
	if id != "" {
		return "pending-" + id
	}
	suffix := strings.TrimPrefix(pane, "%")
	if suffix == "" {
		suffix = pane
	}
	return "pending-" + suffix
}

func rosterDisplayName(r rosterEntry) string {
	if r.Name != "" {
		return r.Name
	}
	return rosterPendingName(r.Pane, r.ID)
}

func parseRosterLines(out string) []rosterEntry {
	var rows []rosterEntry
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		p := strings.Split(line, rosterSep)
		for len(p) < 8 {
			p = append(p, "")
		}
		name := strings.TrimSpace(p[1])
		if name == "" {
			if !isMuxaPane("", p[2], p[3], p[4]) {
				continue
			}
			name = rosterPendingName(p[0], p[2])
		}
		rows = append(rows, rosterEntry{
			Pane: p[0], Name: name, ID: p[2], Parent: p[3], Kind: p[4],
			Where: p[5], Cwd: p[6], Cmd: p[7],
		})
	}
	return rows
}

func liveWorkersOnCwd(rows []rosterEntry, wantReal string) []string {
	var names []string
	for _, r := range rows {
		if r.Parent == "" || r.Cwd == "" {
			continue
		}
		if paneStatus(r.Kind, r.Cwd, r.Cmd) != "live" {
			continue
		}
		other, err := absDir(r.Cwd)
		if err != nil || other != wantReal {
			continue
		}
		names = append(names, rosterDisplayName(r))
	}
	return names
}

func findPaneByName(rows []rosterEntry, name string) (string, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r.Pane, true
		}
	}
	return "", false
}

func findPaneByTarget(rows []rosterEntry, target string) (string, bool) {
	if p, ok := findPaneByName(rows, target); ok {
		return p, true
	}
	for _, r := range rows {
		if r.ID == target {
			return r.Pane, true
		}
	}
	return "", false
}

func nameParent(rows []rosterEntry, name string) string {
	p, ok := findPaneByName(rows, name)
	if !ok {
		return ""
	}
	for _, r := range rows {
		if r.Pane == p {
			return r.Parent
		}
	}
	return ""
}

func nameTakenByOther(rows []rosterEntry, name, self string) bool {
	for _, r := range rows {
		if r.Name == name && r.Pane != self {
			return true
		}
	}
	return false
}

func canSend(rows []rosterEntry, from, to string) bool {
	if from == "" || to == "" || from == to {
		return false
	}
	if nameParent(rows, to) == from {
		return true
	}
	if nameParent(rows, from) == to {
		return true
	}
	return false
}

func parentWouldCycle(rows []rosterEntry, child, parent string) bool {
	if parent == "" {
		return false
	}
	if child == parent {
		return true
	}
	walk := parent
	seen := map[string]bool{}
	for walk != "" {
		if walk == child {
			return true
		}
		if seen[walk] {
			return true
		}
		seen[walk] = true
		walk = nameParent(rows, walk)
	}
	return false
}

func generateUniqueName(rows []rosterEntry, self string) string {
	for i := 0; i < 64; i++ {
		adj := nameAdjectives[mrand.Intn(len(nameAdjectives))]
		noun := nameNouns[mrand.Intn(len(nameNouns))]
		candidate := sanitizeName(adj + "-" + noun)
		if candidate != "" && !nameTakenByOther(rows, candidate, self) {
			return candidate
		}
	}
	adj := nameAdjectives[mrand.Intn(len(nameAdjectives))]
	noun := nameNouns[mrand.Intn(len(nameNouns))]
	suffix := 2
	candidate := sanitizeName(fmt.Sprintf("%s-%s-%d", adj, noun, suffix))
	for nameTakenByOther(rows, candidate, self) {
		suffix++
		candidate = sanitizeName(fmt.Sprintf("%s-%s-%d", adj, noun, suffix))
	}
	return candidate
}

func defaultName(t *TMUX, rows []rosterEntry, pane string) string {
	if n := os.Getenv("MUXA_NAME"); n != "" {
		return sanitizeName(n)
	}
	existing, _ := t.fmt(pane, "#{@muxa_name}")
	if existing != "" {
		return existing
	}
	return generateUniqueName(rows, pane)
}

func paneIsRegistered(t *TMUX, pane string) bool {
	n, _ := t.fmt(pane, "#{@muxa_name}")
	return n != ""
}

func cmdRegister(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	t := NewTMUX()
	pane, err := thisPane(t)
	if err != nil {
		return err
	}
	var name, kind, parent, id string
	for len(args) > 0 {
		if len(args) < 2 {
			return dief("register: unknown arg %s", args[0])
		}
		switch args[0] {
		case "--name":
			name, args = args[1], args[2:]
		case "--kind":
			kind, args = args[1], args[2:]
		case "--parent":
			parent, args = args[1], args[2:]
		case "--id":
			id, args = args[1], args[2:]
		default:
			return dief("register: unknown arg %s", args[0])
		}
	}
	rows, _ := loadRoster(t)
	if name == "" {
		name = defaultName(t, rows, pane)
	}
	if name == "" {
		return die("could not derive a name; pass --name")
	}
	if kind == "" {
		kind = detectKind(t, pane)
	}
	if parent == "" {
		parent = os.Getenv("MUXA_PARENT")
	}
	if parent == "" {
		parent, _ = t.fmt(pane, "#{@muxa_parent}")
	}
	if id == "" {
		id = os.Getenv("MUXA_ID")
	}
	if id == "" {
		id, _ = t.fmt(pane, "#{@muxa_id}")
	}
	if id == "" {
		id = newMuxaID()
	}
	switch kind {
	case "claude", "cursor", "pi", "generic":
	default:
		return dief("bad kind %s", kind)
	}
	if nameTakenByOther(rows, name, pane) {
		return dief("name '%s' already registered on another pane", name)
	}
	if parent != "" {
		if parent == name {
			return die("parent cannot be self")
		}
		if _, ok := findPaneByName(rows, parent); !ok {
			return dief("unknown parent '%s'", parent)
		}
		if parentWouldCycle(rows, name, parent) {
			return dief("parent '%s' would create a cycle", parent)
		}
	}
	if err := t.SetPaneOpt(pane, "@muxa_name", name); err != nil {
		return die(err.Error())
	}
	_ = t.SetPaneOpt(pane, "@muxa_id", id)
	_ = t.SetPaneOpt(pane, "@muxa_kind", kind)
	if parent != "" {
		_ = t.SetPaneOpt(pane, "@muxa_parent", parent)
	} else {
		t.UnsetPaneOpt(pane, "@muxa_parent")
	}
	t.SetTitle(pane, name)
	if parent != "" {
		fmt.Printf("registered %s id=%s parent=%s kind=%s pane=%s\n", name, id, parent, kind, pane)
	} else {
		fmt.Printf("registered %s id=%s parent=- kind=%s pane=%s\n", name, id, kind, pane)
	}
	return nil
}

func cmdWho(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	jsonOut := false
	for len(args) > 0 {
		switch args[0] {
		case "--json":
			jsonOut = true
			args = args[1:]
		default:
			if strings.HasPrefix(args[0], "-") {
				return dief("who: unknown flag %s", args[0])
			}
			return dief("who: unexpected argument %s", args[0])
		}
	}
	t := NewTMUX()
	rows, err := loadRoster(t)
	if err != nil {
		return die(err.Error())
	}
	drawing := brokerDrawingIDs(t)
	if jsonOut {
		out := make([]whoJSONRow, 0, len(rows))
		for _, r := range rows {
			st := paneWhoState(r.Kind, r.Cwd, r.Cmd, r.Pane, drawing)
			out = append(out, whoJSONRow{
				Name: r.Name, ID: r.ID, Parent: strPtr(r.Parent), Kind: r.Kind,
				State: st, Pane: r.Pane, Session: nil, Cwd: r.Cwd,
			})
		}
		if out == nil {
			out = []whoJSONRow{}
		}
		writeJSON(out)
		return nil
	}
	fmt.Printf("%-16s %-14s %-36s %-16s %-8s %-8s %-8s %-16s %s\n",
		"NAME", "ID", "SESSION", "PARENT", "KIND", "STATE", "PANE", "WHERE", "CWD")
	for _, r := range rows {
		st := paneWhoState(r.Kind, r.Cwd, r.Cmd, r.Pane, drawing)
		fmt.Printf("%-16s %-14s %-36s %-16s %-8s %-8s %-8s %-16s %s\n",
			r.Name, r.ID, "", r.Parent, r.Kind, st, r.Pane, r.Where, r.Cwd)
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func cmdWhoami() error {
	if err := needTmux(); err != nil {
		return err
	}
	t := NewTMUX()
	pane, err := thisPane(t)
	if err != nil {
		return err
	}
	n, _ := t.fmt(pane, "#{@muxa_name}")
	if n == "" {
		return die("this pane is not registered (muxa register)")
	}
	fmt.Println(n)
	return nil
}

func cmdParent() error {
	if err := needTmux(); err != nil {
		return err
	}
	t := NewTMUX()
	pane, err := thisPane(t)
	if err != nil {
		return err
	}
	n, _ := t.fmt(pane, "#{@muxa_parent}")
	fmt.Println(n)
	return nil
}

func cmdTail(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	var name, nStr string
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-n":
			if len(args) < 2 {
				return die("tail: -n requires a line count")
			}
			nStr, args = args[1], args[2:]
		case strings.HasPrefix(a, "-n") && a != "-n":
			nStr = strings.TrimPrefix(a, "-n")
			if nStr == "" {
				return die("tail: -n requires a line count")
			}
			args = args[1:]
		case a == "--":
			args = args[1:]
		case strings.HasPrefix(a, "-"):
			return dief("tail: unknown flag %s", a)
		default:
			if name != "" {
				return dief("tail: unexpected argument %s", a)
			}
			name, args = a, args[1:]
		}
	}
	if name == "" {
		return die("tail: NAME required")
	}
	var n int
	if nStr != "" {
		v, err := strconv.Atoi(nStr)
		if err != nil || v <= 0 {
			return die("tail: -n must be a positive integer")
		}
		n = v
	}
	t := NewTMUX()
	rows, _ := loadRoster(t)
	pane, ok := findPaneByName(rows, name)
	if !ok {
		return dief("unknown agent '%s' — muxa who", name)
	}
	var out string
	var err error
	if n > 0 {
		out, err = t.CaptureHistory(pane)
		if err != nil {
			return dief("tail: could not read pane %s", pane)
		}
		fmt.Print(lastNContentLines(out, n))
		return nil
	}
	out, err = t.CapturePlain(pane)
	if err != nil {
		return dief("tail: could not read pane %s", pane)
	}
	fmt.Print(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

func lastNContentLines(s string, n int) string {
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	if start >= end {
		return ""
	}
	return strings.Join(lines[start:end], "\n") + "\n"
}

func cmdKill(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	if len(args) < 1 || args[0] == "" {
		return die("kill: NAME or ID required")
	}
	target := args[0]
	t := NewTMUX()
	rows, _ := loadRoster(t)
	pane, ok := findPaneByTarget(rows, target)
	if !ok {
		return dief("unknown agent '%s' — muxa who", target)
	}
	name, _ := t.fmt(pane, "#{@muxa_name}")
	id, _ := t.fmt(pane, "#{@muxa_id}")
	if err := t.KillPane(pane); err != nil {
		return dief("kill: could not remove pane %s", pane)
	}
	fmt.Printf("killed %s id=%s pane=%s\n", name, id, pane)
	return nil
}

func absDir(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}

func formatOne(from, flags, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[muxa] from=%s\n", from)
	fmt.Fprintf(&b, "%s\n", body)
	if strings.Contains(" "+flags+" ", " no-reply ") {
		b.WriteString("Do not reply.\n")
	} else {
		fmt.Fprintf(&b, "Reply: muxa send %s \"…\"  (skip acks unless asked)\n", from)
	}
	return b.String()
}
