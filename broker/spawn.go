package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type spawnResult struct {
	Name, ID, Pane, Cwd, Kind, Parent string
}

func cmdSpawn(args []string) error {
	res, err := spawnChild(args)
	if err != nil {
		return err
	}
	fmt.Printf("spawned %s id=%s parent=%s kind=%s pane=%s cwd=%s\n",
		res.Name, res.ID, res.Parent, res.Kind, res.Pane, res.Cwd)
	return nil
}

func cmdDispatch(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	briefFile := ""
	var spawnArgs []string
	for len(args) > 0 {
		switch args[0] {
		case "--brief-file":
			if len(args) < 2 {
				return die("dispatch: --brief-file requires a path")
			}
			briefFile, args = args[1], args[2:]
		case "--":
			spawnArgs = append(spawnArgs, args...)
			args = nil
		default:
			spawnArgs = append(spawnArgs, args[0])
			args = args[1:]
		}
	}
	var brief string
	if briefFile != "" {
		st, err := os.Stat(briefFile)
		if err != nil || st.IsDir() {
			return dief("dispatch: --brief-file is not a file: %s", briefFile)
		}
		b, err := os.ReadFile(briefFile)
		if err != nil {
			return die(err.Error())
		}
		brief = string(b)
	} else {
		fi, err := os.Stdin.Stat()
		if err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return die("dispatch: brief required (--brief-file FILE or stdin)")
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return die(err.Error())
		}
		brief = string(b)
	}
	if strings.TrimSpace(brief) == "" {
		return die("dispatch: empty brief")
	}
	res, err := spawnChild(spawnArgs)
	if err != nil {
		return err
	}
	t := NewTMUX()
	parentPane, err := thisPane(t)
	if err != nil {
		return err
	}
	text := formatOne(res.Parent, "", brief)
	id := newSendID()
	if err := ensureBroker(t); err != nil {
		return die("broker unreachable (could not start muxa-broker)")
	}
	if err := brokerEnqueue(t, id, res.Pane, res.Parent, res.Name, text, "dispatch", parentPane); err != nil {
		return die("broker enqueue failed")
	}
	writeJSON(map[string]any{
		"name":  res.Name,
		"id":    id,
		"pane":  res.Pane,
		"cwd":   res.Cwd,
		"state": "dispatched",
		"from":  res.Parent,
		"to":    res.Name,
	})
	return nil
}

func spawnChild(args []string) (spawnResult, error) {
	var zero spawnResult
	if err := needTmux(); err != nil {
		return zero, err
	}
	t := NewTMUX()
	pane, err := thisPane(t)
	if err != nil {
		return zero, err
	}
	parent, _ := t.fmt(pane, "#{@muxa_name}")
	if parent == "" {
		return zero, die("spawn: register this pane first")
	}
	name, kind, cwdFlag := "", "", ""
	newWindow := false
	explicitCmd := false
	for len(args) > 0 {
		switch args[0] {
		case "--name":
			if len(args) < 2 {
				return zero, die("spawn: --name requires a value")
			}
			name, args = args[1], args[2:]
		case "--kind":
			if len(args) < 2 {
				return zero, die("spawn: --kind requires a value")
			}
			kind, args = args[1], args[2:]
		case "--cwd":
			if len(args) < 2 {
				return zero, die("spawn: --cwd requires a value")
			}
			cwdFlag, args = args[1], args[2:]
		case "--window":
			newWindow = true
			args = args[1:]
		case "--":
			explicitCmd = true
			args = args[1:]
			goto parsed
		default:
			if strings.HasPrefix(args[0], "-") {
				return zero, dief("spawn: unknown flag %s", args[0])
			}
			goto parsed
		}
	}
parsed:
	if !explicitCmd && len(args) > 1 {
		for _, arg := range args[1:] {
			if arg == "--" {
				break
			}
			switch arg {
			case "--name", "--kind", "--cwd", "--window":
				return zero, dief("spawn: %s must precede the command (use: muxa spawn --name NAME --cwd DIR -- COMMAND…)", arg)
			}
		}
	}
	if len(args) < 1 {
		return zero, die("spawn: command required (muxa spawn --name worker -- cmd…)")
	}
	id := newMuxaID()
	rows, _ := loadRoster(t)
	if name != "" {
		name = sanitizeName(name)
	} else {
		name = generateUniqueName(rows, "")
	}
	if kind == "" {
		kind = kindFromCommand(args[0])
	}
	if nameTakenByOther(rows, name, "") {
		return zero, dief("name '%s' already registered", name)
	}
	cwd, err := resolveSpawnCwd(t, cwdFlag, pane)
	if err != nil {
		return zero, err
	}
	if cwd == "" {
		return zero, die("spawn: could not resolve a start directory")
	}
	warnOccupiedSpawnCwd(t, cwd)
	sess, _ := t.fmt(pane, "#{session_name}")
	quoted := shlexJoin(args...)
	cmdpath, err := exec.LookPath("muxa")
	if err != nil || cmdpath == "" {
		cmdpath = "muxa"
	}
	envcmd := "export PATH=" + shlexJoin(filepath.Dir(cmdpath)+":"+os.Getenv("PATH")) +
		" MUXA_NAME=" + shlexJoin(name) +
		" MUXA_PARENT=" + shlexJoin(parent) +
		" MUXA_ID=" + shlexJoin(id)
	for _, kv := range []struct{ k, v string }{
		{"MUXA_HOOK_LOG", os.Getenv("MUXA_HOOK_LOG")},
		{"MUXA_BIN", os.Getenv("MUXA_BIN")},
		{"MUXA_BROKER_DIR", os.Getenv("MUXA_BROKER_DIR")},
		{"MUXA_BROKER_SOCK", os.Getenv("MUXA_BROKER_SOCK")},
		{"MUXA_BROKER_PID", os.Getenv("MUXA_BROKER_PID")},
		{"MUXA_BROKER_BIN", os.Getenv("MUXA_BROKER_BIN")},
		{"XDG_RUNTIME_DIR", os.Getenv("XDG_RUNTIME_DIR")},
	} {
		if kv.v != "" {
			envcmd += " " + kv.k + "=" + shlexJoin(kv.v)
		}
	}
	spawnTmux := "tmux"
	if sock := os.Getenv("MUXA_TMUX_SOCKET"); sock != "" {
		envcmd += " MUXA_TMUX_SOCKET=" + shlexJoin(sock)
		spawnTmux = "tmux -L " + shlexJoin(sock)
	}
	if kind == "cursor" && !strings.Contains(" "+quoted+" ", " --trust ") {
		quoted = quoted + " --trust"
	}
	envcmd += "; " + spawnTmux + " set-window-option remain-on-exit on 2>/dev/null || true; exec " + quoted

	var child string
	if newWindow {
		child, err = t.NewWindow(sess, name, cwd, envcmd)
		if err != nil || child == "" {
			return zero, die("spawn: tmux did not return a pane id")
		}
	} else {
		winid, _ := t.fmt(pane, "#{window_id}")
		child, _ = t.SplitWindow("-h", pane, cwd, envcmd)
		if child == "" {
			child, _ = t.SplitWindow("-v", pane, cwd, envcmd)
		}
		if child == "" {
			largest := t.LargestPane(pane)
			if largest == "" {
				largest = pane
			}
			child, _ = t.SplitWindow("-h", largest, cwd, envcmd)
			if child == "" {
				child, _ = t.SplitWindow("-v", largest, cwd, envcmd)
			}
		}
		t.SelectLayout(winid, "tiled")
		if child == "" {
			return zero, die("spawn: tmux did not return a pane id")
		}
	}
	childWin, _ := t.fmt(child, "#{session_name}:#{window_index}")
	t.SetWindowOption(childWin, "remain-on-exit", "on")
	_ = t.SetPaneOpt(child, "@muxa_name", name)
	_ = t.SetPaneOpt(child, "@muxa_id", id)
	_ = t.SetPaneOpt(child, "@muxa_parent", parent)
	_ = t.SetPaneOpt(child, "@muxa_kind", kind)
	t.SetTitle(child, name)
	return spawnResult{Name: name, ID: id, Pane: child, Cwd: cwd, Kind: kind, Parent: parent}, nil
}

func resolveSpawnCwd(t *TMUX, explicit, pane string) (string, error) {
	if explicit != "" {
		st, err := os.Stat(explicit)
		if err != nil || !st.IsDir() {
			return "", dief("spawn: --cwd is not a directory: %s", explicit)
		}
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", die(err.Error())
		}
		return abs, nil
	}
	if pwd, err := os.Getwd(); err == nil {
		if st, err := os.Stat(pwd); err == nil && st.IsDir() {
			return pwd, nil
		}
	}
	p, _ := t.fmt(pane, "#{pane_current_path}")
	return p, nil
}

func warnOccupiedSpawnCwd(t *TMUX, want string) {
	wantReal, err := absDir(want)
	if err != nil {
		return
	}
	rows, err := loadRoster(t)
	if err != nil {
		return
	}
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
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "muxa: warning: cwd %s already has live worker %s\n", want, strings.Join(names, ", "))
}
