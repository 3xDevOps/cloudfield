package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net"
	"slices"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/testhome"
)

// fakeReply is one served method: what it answers and the params of the last
// request it received.
type fakeReply struct {
	mu     sync.Mutex
	result any
	err    *protocol.Error
	got    json.RawMessage
}

func (r *fakeReply) answer(result any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
}

func (r *fakeReply) params() json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got
}

// fakeControlMethods serves every method in replies; any other method is
// answered with a method-not-found error, so a test proves which RPCs ran.
func fakeControlMethods(t *testing.T, replies map[string]*fakeReply) *protocol.Client {
	t.Helper()
	server, client := net.Pipe()
	go serveFakeControl(server, replies)
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func serveFakeControl(server io.ReadWriteCloser, replies map[string]*fakeReply) {
	defer server.Close() //nolint:errcheck
	r := bufio.NewReader(server)
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
		reply, served := replies[req.Method]
		if !served {
			resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected method " + req.Method}
		} else {
			reply.mu.Lock()
			reply.got = append(json.RawMessage(nil), req.Params...)
			resp.Error = reply.err
			if reply.err == nil {
				resp.Result, _ = json.Marshal(reply.result)
			}
			reply.mu.Unlock()
		}
		out, _ := json.Marshal(resp)
		if _, err := server.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// fakeLinkedServer links the CLI to an SSH server that serves replies on
// the control subsystem, refuses every other subsystem, and returns the
// subsystems commands asked it for.
func fakeLinkedServer(t *testing.T, replies map[string]*fakeReply) func() []string {
	t.Helper()
	testhome.Isolate(t)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{NoClientAuth: true}
	conf.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var asked []string
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, conf)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, chReqs, err := nch.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range chReqs {
							var sub struct{ Subsystem string }
							if req.Type != "subsystem" || ssh.Unmarshal(req.Payload, &sub) != nil {
								_ = req.Reply(false, nil)
								continue
							}
							mu.Lock()
							asked = append(asked, sub.Subsystem)
							mu.Unlock()
							_ = req.Reply(sub.Subsystem == protocol.SubsystemControl, nil)
							if sub.Subsystem == protocol.SubsystemControl {
								go serveFakeControl(ch, replies)
							}
						}
					}()
				}
			}()
		}
	}()
	if err := cli.Save(cli.Config{Addr: ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}
