// Package sshtest is an SSH server for tests. It accepts one key and runs
// what it is asked to with this machine's shell, so the SSH runner can be
// tried against the real protocol without a second machine.
//
// It executes any command a holder of the key sends. It is for tests only.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is a running test server.
type Server struct {
	Host    string
	Port    int
	Signer  ssh.Signer    // the key clients sign in with
	HostKey ssh.PublicKey // the key the server presents

	ln net.Listener
	mu sync.Mutex
	// Commands are the command lines received, in order.
	commands []string
	conns    []net.Conn
	accepted int
	silent   map[string]bool
}

// What a server can be made to stop answering, for Silence.
const (
	Sessions  = "sessions"  // a request to open a session
	Exec      = "exec"      // a request to run a command
	Keepalive = "keepalive" // a keepalive request
)

// Silence makes the server say nothing to the given kinds of request from
// now on, as a machine that has hung would: the connection stays open and
// the answer never comes.
func (s *Server) Silence(what ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.silent == nil {
		s.silent = map[string]bool{}
	}
	for _, w := range what {
		s.silent[w] = true
	}
}

func (s *Server) isSilent(what string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.silent[what]
}

// Accepted is how many connections the server has accepted so far.
func (s *Server) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func newSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// Start runs a server on a loopback port until the test ends. It accepts a
// key generated for it, available as Signer.
func Start(t testing.TB) *Server {
	t.Helper()
	return StartWithKey(t, newSigner(t))
}

// StartWithKey is Start for a server that accepts the given key.
func StartWithKey(t testing.TB, client ssh.Signer) *Server {
	t.Helper()
	hostSigner := newSigner(t)
	s := &Server{Host: "127.0.0.1", Signer: client, HostKey: hostSigner.PublicKey()}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(s.Signer.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.Port = ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(s.Close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.accepted++
			s.mu.Unlock()
			go s.serve(conn, cfg)
		}
	}()
	return s
}

// Close stops the server and drops every connection.
func (s *Server) Close() {
	s.ln.Close()
	s.DropConnections()
}

// DropConnections cuts every open connection, as a network failure would.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

// Commands returns the command lines received so far.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *Server) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sc.Close()
	go func() {
		for req := range reqs {
			if req.WantReply && !s.isSilent(Keepalive) {
				req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for ch := range chans {
		switch ch.ChannelType() {
		case "session":
			if s.isSilent(Sessions) {
				continue
			}
			channel, requests, err := ch.Accept()
			if err == nil {
				go s.session(channel, requests)
			}
		case "direct-tcpip":
			go forward(ch)
		default:
			ch.Reject(ssh.UnknownChannelType, "not supported")
		}
	}
}

// forward serves one forwarded connection: what Runner.Dial opens.
func forward(ch ssh.NewChannel) {
	var req struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(ch.ExtraData(), &req); err != nil {
		ch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
	if err != nil {
		ch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, requests, err := ch.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go func() { io.Copy(target, channel); target.Close() }()
	io.Copy(channel, target)
	channel.Close()
}

func (s *Server) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	var cmd *exec.Cmd
	done := make(chan struct{})
	for {
		select {
		case <-done:
			return
		case req, ok := <-requests:
			if !ok {
				// The client went away. A real sshd would leave the command
				// running; the runner is expected to have killed it.
				return
			}
			switch req.Type {
			case "exec":
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil || cmd != nil {
					req.Reply(false, nil)
					continue
				}
				if s.isSilent(Exec) {
					continue
				}
				s.mu.Lock()
				s.commands = append(s.commands, payload.Command)
				s.mu.Unlock()
				cmd = exec.Command("sh", "-c", payload.Command)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Stdout = channel
				cmd.Stderr = channel.Stderr()
				stdin, _ := cmd.StdinPipe()
				if err := cmd.Start(); err != nil {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				go func() {
					io.Copy(stdin, channel)
					stdin.Close()
				}()
				go func() {
					err := cmd.Wait()
					code := 0
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
						if code < 0 {
							code = 137
						}
					} else if err != nil {
						code = 127
					}
					status := make([]byte, 4)
					binary.BigEndian.PutUint32(status, uint32(code))
					channel.SendRequest("exit-status", false, status)
					channel.CloseWrite()
					close(done)
				}()
			case "signal":
				if cmd != nil && cmd.Process != nil {
					syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			default:
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		}
	}
}
