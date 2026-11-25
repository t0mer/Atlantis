// Package fakessh is an in-process SSH server for tests: it accepts one
// user/password and connects direct-tcpip channels for 127.0.0.1:20002 to a
// fake TMP Deco.
package fakessh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/t0mer/atlantis/internal/deco/tmp/fakedeco"
)

// Server is the fake.
type Server struct {
	Device *fakedeco.Device
	Key    ssh.Signer

	ln       net.Listener
	mu       sync.Mutex
	attempts int
	targets  []string
}

// New starts a server on 127.0.0.1 accepting user/password.
func New(user, password string) (*Server, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{Device: fakedeco.New(), Key: signer, ln: ln}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			s.attempts++
			s.mu.Unlock()
			if c.User() == user && string(p) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg)
		}
	}()
	return s, nil
}

// Addr returns the host and port to dial.
func (s *Server) Addr() (string, int) {
	host, port, _ := net.SplitHostPort(s.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return host, p
}

// Close stops accepting connections.
func (s *Server) Close() error { return s.ln.Close() }

// Attempts counts password authentication attempts.
func (s *Server) Attempts() int { s.mu.Lock(); defer s.mu.Unlock(); return s.attempts }

// Targets lists the host:port of every direct-tcpip channel opened.
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *Server) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if nch.ChannelType() != "direct-tcpip" || ssh.Unmarshal(nch.ExtraData(), &p) != nil {
			nch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		s.mu.Lock()
		s.targets = append(s.targets, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
		s.mu.Unlock()
		if p.Host != "127.0.0.1" || p.Port != 20002 {
			nch.Reject(ssh.ConnectionFailed, "only the TMP service is forwarded")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { s.Device.Serve(ch); ch.Close() }()
	}
}
