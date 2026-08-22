package main

import (
	"fmt"
	"io"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func newSendID() string {
	return fmt.Sprintf("%d-%d-%05d", time.Now().Unix(), os.Getpid(), mrand.Intn(100000))
}

func brokerBinPath() (string, error) {
	if p := os.Getenv("MUXA_BROKER_BIN"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return exe, nil
}

func signDarwin(p string) {
	if runtime.GOOS != "darwin" {
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return
	}
	need := exec.Command("xattr", "-p", "com.apple.provenance", p).Run() == nil
	if !need {
		need = exec.Command("codesign", "--verify", p).Run() != nil
	}
	if !need {
		return
	}
	_ = exec.Command("xattr", "-c", p).Run()
	_ = exec.Command("codesign", "-s", "-", "--force", "--timestamp=none", p).Run()
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new." + strconv.Itoa(os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func brokerPing(p brokerPaths) bool {
	st, err := os.Stat(p.Sock)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return false
	}
	old := os.Getenv("MUXA_BROKER_SOCK")
	_ = os.Setenv("MUXA_BROKER_SOCK", p.Sock)
	rc := cliPing()
	if old == "" {
		_ = os.Unsetenv("MUXA_BROKER_SOCK")
	} else {
		_ = os.Setenv("MUXA_BROKER_SOCK", old)
	}
	return rc == 0
}

func brokerDrawingIDs(t *TMUX) []string {
	p := setupBrokerPaths(t)
	st, err := os.Stat(p.Sock)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return nil
	}
	old := os.Getenv("MUXA_BROKER_SOCK")
	_ = os.Setenv("MUXA_BROKER_SOCK", p.Sock)
	resp, err := rpcClient(Request{Op: "status"}, 200*time.Millisecond)
	if old == "" {
		_ = os.Unsetenv("MUXA_BROKER_SOCK")
	} else {
		_ = os.Setenv("MUXA_BROKER_SOCK", old)
	}
	if err != nil || !resp.OK {
		return nil
	}
	return resp.Drawing
}

func ensureBroker(t *TMUX) error {
	p := setupBrokerPaths(t)
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	if brokerPing(p) {
		return nil
	}
	bin, err := brokerBinPath()
	if err != nil {
		return err
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return fmt.Errorf("broker binary not executable: %s", bin)
	}
	runbin := filepath.Join(p.Dir, "muxa-broker")
	if err := copyExecutable(bin, runbin); err != nil {
		return err
	}
	_ = os.Chmod(runbin, 0o755)
	signDarwin(runbin)
	logf, err := os.OpenFile(filepath.Join(p.Dir, "broker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(runbin)
	cmd.Env = append(withoutEnv(os.Environ(),
		brokerEntryEnv, daemonEnv, "MUXA_BROKER_FOREGROUND",
		"MUXA_BROKER_DIR", "MUXA_BROKER_SOCK", "MUXA_BROKER_PID",
	),
		brokerEntryEnv+"=1",
		daemonEnv+"=0",
		"MUXA_BROKER_FOREGROUND=0",
		"MUXA_BROKER_DIR="+p.Dir,
		"MUXA_BROKER_SOCK="+p.Sock,
		"MUXA_BROKER_PID="+p.PID,
	)
	cmd.Dir = "/"
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if brokerPing(p) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("broker did not start")
}

func brokerEnqueue(t *TMUX, id, pane, from, to, text, kind, parentPane string) error {
	p := setupBrokerPaths(t)
	old := os.Getenv("MUXA_BROKER_SOCK")
	_ = os.Setenv("MUXA_BROKER_SOCK", p.Sock)
	defer func() {
		if old == "" {
			_ = os.Unsetenv("MUXA_BROKER_SOCK")
		} else {
			_ = os.Setenv("MUXA_BROKER_SOCK", old)
		}
	}()
	req := Request{
		Op: "enqueue", ID: id, Pane: pane, From: from, To: to, Text: text,
		Kind: kind, ParentPane: parentPane,
	}
	resp, err := rpcClient(req, 3*time.Second)
	if err != nil {
		return err
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = "enqueue failed"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func cmdSend(args []string) error {
	if err := needTmux(); err != nil {
		return err
	}
	noReply, jsonOut := false, false
	bodyFile := ""
	for len(args) > 0 {
		switch args[0] {
		case "--no-reply":
			noReply = true
			args = args[1:]
		case "--json":
			jsonOut = true
			args = args[1:]
		case "--file":
			if len(args) < 2 {
				return die("send: --file requires a path")
			}
			bodyFile, args = args[1], args[2:]
		case "--":
			args = args[1:]
			goto body
		default:
			if strings.HasPrefix(args[0], "-") {
				return dief("send: unknown flag %s", args[0])
			}
			goto body
		}
	}
body:
	if len(args) < 1 {
		return die("send: NAME required")
	}
	to := args[0]
	args = args[1:]
	var body string
	switch {
	case bodyFile != "" && len(args) > 0:
		return die("send: --file and positional body are mutually exclusive")
	case bodyFile != "":
		st, err := os.Stat(bodyFile)
		if err != nil || st.IsDir() {
			return dief("send: --file is not a file: %s", bodyFile)
		}
		b, err := os.ReadFile(bodyFile)
		if err != nil {
			return die(err.Error())
		}
		body = string(b)
	case len(args) > 0:
		body = strings.Join(args, " ")
	default:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return die(err.Error())
		}
		body = string(b)
	}
	if body == "" {
		return die("send: empty body")
	}
	t := NewTMUX()
	fromPane, err := thisPane(t)
	if err != nil {
		return err
	}
	from, _ := t.fmt(fromPane, "#{@muxa_name}")
	if from == "" {
		from = env("MUXA_NAME", "human")
	}
	flags := ""
	if noReply {
		flags = "no-reply"
	}
	return sendOne(t, from, to, flags, body, jsonOut)
}

func sendOne(t *TMUX, from, to, flags, body string, jsonOut bool) error {
	if to == from {
		return nil
	}
	rows, _ := loadRoster(t)
	pane, ok := findPaneByName(rows, to)
	if !ok {
		return dief("unknown agent '%s' — muxa who", to)
	}
	if !canSend(rows, from, to) {
		return forbid(fmt.Sprintf("forbidden %s → %s (only parent↔child)", from, to))
	}
	text := formatOne(from, flags, body)
	id := newSendID()
	if err := ensureBroker(t); err != nil {
		return die("broker unreachable (could not start muxa-broker)")
	}
	if err := brokerEnqueue(t, id, pane, from, to, text, "", ""); err != nil {
		return die("broker enqueue failed")
	}
	if jsonOut {
		writeJSON(map[string]string{"id": id, "pane": pane, "from": from, "to": to})
		return nil
	}
	fmt.Printf("queued %s → %s id=%s (broker)\n", from, to, id)
	return nil
}

func cmdBroker(args []string) error {
	sub := "start"
	if len(args) > 0 {
		sub = args[0]
	}
	if err := needTmux(); err != nil {
		return err
	}
	t := NewTMUX()
	p := setupBrokerPaths(t)
	switch sub {
	case "start":
		if err := ensureBroker(t); err != nil {
			return die("could not start broker (build bin/muxa)")
		}
		pid := "?"
		if b, err := os.ReadFile(p.PID); err == nil {
			pid = strings.TrimSpace(string(b))
			if pid == "" {
				pid = "?"
			}
		}
		fmt.Printf("broker pid=%s sock=%s dir=%s\n", pid, p.Sock, p.Dir)
		return nil
	case "status":
		if brokerPing(p) {
			old := os.Getenv("MUXA_BROKER_SOCK")
			_ = os.Setenv("MUXA_BROKER_SOCK", p.Sock)
			rc := cliStatus()
			if old == "" {
				_ = os.Unsetenv("MUXA_BROKER_SOCK")
			} else {
				_ = os.Setenv("MUXA_BROKER_SOCK", old)
			}
			if rc != 0 {
				return cliErr{code: rc, msg: ""}
			}
			return nil
		}
		fmt.Printf("broker down sock=%s\n", p.Sock)
		return cliErr{code: 1, msg: ""}
	case "stop":
		pid := ""
		if b, err := os.ReadFile(p.PID); err == nil {
			pid = strings.TrimSpace(string(b))
		}
		if n, err := strconv.Atoi(pid); err == nil && n > 1 {
			proc, err := os.FindProcess(n)
			if err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
		_ = os.Remove(p.PID)
		_ = os.Remove(p.Sock)
		if pid == "" {
			pid = "none"
		}
		fmt.Printf("stopped broker pid=%s\n", pid)
		return nil
	default:
		return die("broker: start|status|stop")
	}
}
