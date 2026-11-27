package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestMessagePrintsTheReceipt(t *testing.T) {
	after := "2026-10-25T06:00:45Z"
	for _, tc := range []struct {
		message protocol.RoomMessage
		out     string
		err     string
	}{
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageSent}, out: "message m1 delivered to session r1\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageSent, AgentDelivery: "queued"},
			out: "message m1 sent to session r1, queued behind the agent's current turn\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageQueued, DeliverAfter: &after},
			out: "message m1 queued for the member in control of session r1, who can approve or deny it; otherwise it is delivered at " + after + "\n"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageDenied, Failure: &protocol.RoomMessageFailure{Message: "denied by the controller"}},
			err: "message m1 to session r1 was denied: denied by the controller"},
		{message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageNotSent}, err: "message m1 to session r1 was not sent"},
	} {
		c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunInject: {result: protocol.RunRoomPostResult{Message: tc.message}}})
		var out bytes.Buffer
		err := sendMessage(c, &out, "r1", "hi", nil, false, "k-1")
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("%s message: error %v, want %q", tc.message.State, err, tc.err)
			}
			continue
		}
		if err != nil || out.String() != tc.out {
			t.Errorf("%s message: printed %q (%v), want %q", tc.message.State, out.String(), err, tc.out)
		}
	}
}

func TestMessageSteersAndAttachesUploadedFiles(t *testing.T) {
	dir := t.TempDir()
	shot, notes := filepath.Join(dir, "shot.png"), filepath.Join(dir, "notes.txt")
	png, text := []byte("\x89PNG\r\n\x1a\npixels"), []byte("line one\n")
	if err := os.WriteFile(shot, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, text, 0o600); err != nil {
		t.Fatal(err)
	}
	const imageRef, fileRef = "/root/.aether/terminal-images/a.png", "/root/.aether/terminal-files/b/notes.txt"
	replies := map[string]*fakeReply{
		protocol.MethodTerminalImage: {result: protocol.TerminalUploadResult{Path: imageRef}},
		protocol.MethodTerminalFile:  {result: protocol.TerminalUploadResult{Path: fileRef}},
		protocol.MethodRunInject:     {result: protocol.RunRoomPostResult{Message: protocol.RoomMessage{ID: "m1", State: protocol.RoomMessageSent}}},
	}
	inject := func(stdin string, args ...string) protocol.RunInjectParams {
		t.Helper()
		send, err := parseMessage(args, strings.NewReader(stdin))
		if err != nil {
			t.Fatal(err)
		}
		if err := send(fakeControlMethods(t, replies), io.Discard); err != nil {
			t.Fatal(err)
		}
		var sent protocol.RunInjectParams
		if err := json.Unmarshal(replies[protocol.MethodRunInject].params(), &sent); err != nil || sent.IdempotencyKey == "" {
			t.Fatalf("run.inject params = %s (%v), want an idempotency key", replies[protocol.MethodRunInject].params(), err)
		}
		return sent
	}
	sent := inject("", "--steer", "--attach", shot, "--attach", notes, "r1", "--steer=true is ignored,", "-v", "too")
	for method, want := range map[string]any{
		protocol.MethodTerminalImage: protocol.TerminalImageParams{RunID: "r1", Content: base64.StdEncoding.EncodeToString(png)},
		protocol.MethodTerminalFile:  protocol.TerminalFileParams{RunID: "r1", Name: "notes.txt", Content: base64.StdEncoding.EncodeToString(text)},
		protocol.MethodRunInject: protocol.RunInjectParams{RunID: "r1", Message: "--steer=true is ignored, -v too", Attachments: []string{imageRef, fileRef},
			IdempotencyKey: sent.IdempotencyKey, Steer: true},
	} {
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if got := replies[method].params(); string(got) != string(wantJSON) {
			t.Errorf("%s params = %s, want %s", method, got, wantJSON)
		}
	}
	if got := inject("--steer\n", "r1", "-"); got.Message != "--steer" || got.Steer {
		t.Errorf("--steer on stdin sent %+v, want it as the message text", got)
	}
	if got := inject("", "--attach", notes, "r1"); got.Message != "" || !slices.Equal(got.Attachments, []string{fileRef}) {
		t.Errorf("--attach without text sent %+v, want only %s", got, fileRef)
	}

	send, err := parseMessage([]string{"--attach", shot, "r1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(shot, domain.MaxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := send(fakeControlMethods(t, replies), io.Discard); err == nil || !strings.Contains(err.Error(), "shot.png: grew past the 8 MiB image limit") {
		t.Errorf("an image that grew after the size check = %v, want it refused", err)
	}
}

func TestMessageRefusesWhatItCannotSendBeforeDialing(t *testing.T) {
	dir := t.TempDir()
	big, shot := filepath.Join(dir, "big.bin"), filepath.Join(dir, "shot.png")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, configMaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shot, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(shot, domain.MaxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	for want, args := range map[string][]string{
		"big.bin: 67108865 bytes is over the 64 MiB limit":      {"--attach", big, "r1", "hi"},
		"shot.png: 8388609 bytes is over the 8 MiB image limit": {"--attach", shot, "r1", "hi"},
		"attach " + dir + ": not a regular file":                {"--attach", dir, "r1"},
		"message: 9 --attach files, at most 8":                  append(slices.Repeat([]string{"--attach", big}, 9), "r1"),
		"message: --attach comes after the session id":          {"r1", "use this mock", "--attach", "mock.png"},
		"message: --steer comes after the session id":           {"r1", "--steer", "stop editing the README"},
		"message: -attach=mock.png comes after the session id":  {"r1", "-attach=mock.png", "use this mock"},
		"message: --attach=My Shot.png comes after the session": {"r1", "match this", "--attach=My Shot.png"},
		"or pipe a message that starts with it on stdin with -": {"r1", "--steer"},
		"attach: ": {"--attach", filepath.Join(dir, "missing.png"), "r1", "hi"},
	} {
		if _, err := parseMessage(args, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("message %v = %v, want %q", args, err, want)
		}
	}
}

func TestRunActionsPrintTheStatusAndNameMissingRuns(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunPause: {result: struct{}{}},
		protocol.MethodRunGet:   {result: protocol.RunResult{Run: protocol.Run{ID: "r1", Status: "running", Paused: true}}},
		protocol.MethodRunKill:  {err: &protocol.Error{Code: protocol.CodeNotFound, Message: "store: not found"}},
	})
	var out bytes.Buffer
	if err := actOnRun(c, &out, protocol.MethodRunPause, "r1"); err != nil || out.String() != "session r1 paused\n" {
		t.Fatalf("pause printed %q (%v), want session r1 paused", out.String(), err)
	}
	if err := actOnRun(c, &out, protocol.MethodRunKill, "r9"); err == nil || err.Error() != "session r9 not found; aether sessions lists sessions" {
		t.Fatalf("kill of an unknown run = %v", err)
	}
	if strings.Contains(out.String(), "r9") {
		t.Fatalf("kill of an unknown run printed %q", out.String())
	}
}
