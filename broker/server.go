package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

type Request struct {
	Op           string `json:"op"`
	ID           string `json:"id,omitempty"`
	Pane         string `json:"pane,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	Text         string `json:"text,omitempty"`
	DeadlineUnix int64  `json:"deadline_unix,omitempty"`
}

type Response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	ID     string `json:"id,omitempty"`
	State  string `json:"state,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Queued int    `json:"queued,omitempty"`
	Done   int    `json:"done,omitempty"`
	Failed int    `json:"failed,omitempty"`
	Socket string `json:"socket,omitempty"`
}

type Server struct {
	Sock     string
	Q        *Queue
	Deadline time.Duration
	ln       net.Listener
}

// sunPathMax is the smallest sockaddr_un.sun_path across the platforms we
// run on (104 on darwin, 108 on linux). Past it the kernel answers "invalid
// argument", which reads like a bug in the caller rather than a long path.
const sunPathMax = 104

func (s *Server) Listen() error {
	if len(s.Sock) >= sunPathMax {
		return fmt.Errorf("socket path is %d bytes, limit is %d: %s (set MUXA_BROKER_SOCK to something shorter)",
			len(s.Sock), sunPathMax-1, s.Sock)
	}
	if err := os.Remove(s.Sock); err != nil && !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", s.Sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.Sock, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

func (s *Server) Close() error {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	_ = os.Remove(s.Sock)
	return nil
}

func (s *Server) Serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed") {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		s.write(c, Response{OK: false, Error: "read: " + err.Error()})
		return
	}
	line = bytesTrim(line)
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(c, Response{OK: false, Error: "json: " + err.Error()})
		return
	}
	s.write(c, s.dispatch(req))
}

func (s *Server) dispatch(req Request) Response {
	switch req.Op {
	case "ping":
		return Response{OK: true, State: "pong", PID: os.Getpid(), Socket: s.Sock}
	case "status":
		p, d, f, err := s.Q.Counts()
		if err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		return Response{OK: true, PID: os.Getpid(), Queued: p, Done: d, Failed: f, Socket: s.Sock}
	case "enqueue":
		if req.Pane == "" || req.Text == "" {
			return Response{OK: false, Error: "pane and text required"}
		}
		deadline := req.DeadlineUnix
		if deadline == 0 {
			deadline = time.Now().Add(s.Deadline).Unix()
		}
		m := &Msg{
			ID:           req.ID,
			Pane:         req.Pane,
			From:         req.From,
			To:           req.To,
			Text:         req.Text,
			DeadlineUnix: deadline,
		}
		if err := s.Q.Put(m); err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		return Response{OK: true, ID: m.ID, State: "queued"}
	default:
		return Response{OK: false, Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

func (s *Server) write(c net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = c.Write(append(b, '\n'))
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
