package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

func runCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "muxa-broker: ping|status|drawing|enqueue|json-object|json-array|who-json")
		return 2
	}
	switch args[0] {
	case "ping":
		return cliPing()
	case "status":
		return cliStatus()
	case "drawing":
		return cliDrawing()
	case "enqueue":
		return cliEnqueue(args[1:])
	case "json-object":
		return cliJSONObject(args[1:])
	case "json-array":
		return cliJSONArray()
	case "who-json":
		return cliWhoJSON()
	default:
		fmt.Fprintf(os.Stderr, "muxa-broker: unknown command %q\n", args[0])
		return 2
	}
}

func sockPath() (string, error) {
	s := os.Getenv("MUXA_BROKER_SOCK")
	if s == "" {
		return "", fmt.Errorf("MUXA_BROKER_SOCK is required")
	}
	return s, nil
}

func rpcClient(req Request, timeout time.Duration) (Response, error) {
	var zero Response
	sock, err := sockPath()
	if err != nil {
		return zero, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	d := net.Dialer{Timeout: timeout}
	c, err := d.Dial("unix", sock)
	if err != nil {
		return zero, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(append(b, '\n')); err != nil {
		return zero, err
	}
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	data, err := io.ReadAll(c)
	if err != nil {
		return zero, err
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(data), &resp); err != nil {
		return zero, err
	}
	return resp, nil
}

func cliPing() int {
	resp, err := rpcClient(Request{Op: "ping"}, 500*time.Millisecond)
	if err != nil || !resp.OK {
		return 1
	}
	return 0
}

func cliStatus() int {
	resp, err := rpcClient(Request{Op: "status"}, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: %v\n", err)
		return 1
	}
	return writeJSON(resp)
}

func cliDrawing() int {
	resp, err := rpcClient(Request{Op: "status"}, 200*time.Millisecond)
	if err != nil || !resp.OK {
		return 0
	}
	fmt.Print(strings.Join(resp.Drawing, " "))
	if len(resp.Drawing) > 0 {
		fmt.Print("\n")
	}
	return 0
}

func cliEnqueue(args []string) int {
	var id, pane, from, to, kind, parent string
	for len(args) > 0 {
		switch args[0] {
		case "--id":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --id requires a value")
				return 2
			}
			id, args = args[1], args[2:]
		case "--pane":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --pane requires a value")
				return 2
			}
			pane, args = args[1], args[2:]
		case "--from":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --from requires a value")
				return 2
			}
			from, args = args[1], args[2:]
		case "--to":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --to requires a value")
				return 2
			}
			to, args = args[1], args[2:]
		case "--kind":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --kind requires a value")
				return 2
			}
			kind, args = args[1], args[2:]
		case "--parent-pane":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker enqueue: --parent-pane requires a value")
				return 2
			}
			parent, args = args[1], args[2:]
		default:
			fmt.Fprintf(os.Stderr, "muxa-broker enqueue: unknown arg %s\n", args[0])
			return 2
		}
	}
	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: read stdin: %v\n", err)
		return 1
	}
	req := Request{
		Op:         "enqueue",
		ID:         id,
		Pane:       pane,
		From:       from,
		To:         to,
		Text:       string(text),
		Kind:       kind,
		ParentPane: parent,
	}
	resp, err := rpcClient(req, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: %v\n", err)
		return 1
	}
	if !resp.OK {
		errMsg := resp.Error
		if errMsg == "" {
			errMsg = "enqueue failed"
		}
		fmt.Fprintf(os.Stderr, "muxa: broker: %s\n", errMsg)
		return 1
	}
	return 0
}

func cliJSONObject(args []string) int {
	nulls := map[string]bool{}
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		if args[0] == "--null" {
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "muxa-broker json-object: --null requires a key")
				return 2
			}
			nulls[args[1]] = true
			args = args[2:]
			continue
		}
		break
	}
	if len(args)%2 != 0 {
		fmt.Fprintln(os.Stderr, "muxa-broker json-object: keys and values must come in pairs")
		return 2
	}
	obj := map[string]any{}
	for i := 0; i < len(args); i += 2 {
		k, v := args[i], args[i+1]
		if nulls[k] {
			obj[k] = nil
			continue
		}
		obj[k] = v
	}
	for k := range nulls {
		if _, ok := obj[k]; !ok {
			obj[k] = nil
		}
	}
	return writeJSON(obj)
}

func cliJSONArray() int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: read stdin: %v\n", err)
		return 1
	}
	var rows []any
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			fmt.Fprintf(os.Stderr, "muxa-broker json-array: %v\n", err)
			return 1
		}
		rows = append(rows, v)
	}
	if rows == nil {
		rows = []any{}
	}
	return writeJSON(rows)
}

func cliWhoJSON() int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: read stdin: %v\n", err)
		return 1
	}
	var rows []map[string]any
	for _, raw := range strings.Split(string(data), "\n") {
		if raw == "" {
			continue
		}
		p := strings.Split(raw, "||")
		for len(p) < 10 {
			p = append(p, "")
		}
		parent := any(p[3])
		if p[3] == "" {
			parent = nil
		}
		rows = append(rows, map[string]any{
			"name":    p[1],
			"id":      p[2],
			"parent":  parent,
			"kind":    p[4],
			"state":   p[9],
			"pane":    p[0],
			"session": nil,
			"cwd":     p[6],
			"status":  p[8],
		})
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return writeJSON(rows)
}

func writeJSON(v any) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "muxa-broker: json: %v\n", err)
		return 1
	}
	os.Stdout.Write(buf.Bytes())
	return 0
}
