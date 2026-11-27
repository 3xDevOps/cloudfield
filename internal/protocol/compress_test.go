package protocol

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func TestGzipFrameRoundTrip(t *testing.T) {
	t.Parallel()
	result, _ := json.Marshal(RunListResult{Runs: benchRuns(100)})
	resp := Response{JSONRPC: "2.0", ID: json.RawMessage("7"), Result: result}
	plain, _ := json.Marshal(resp)
	for _, compress := range []bool{false, true} {
		var wire bytes.Buffer
		if err := WriteResponse(&wire, resp, compress); err != nil {
			t.Fatal(err)
		}
		wire.WriteString(`{"next":true}` + "\n")
		compressed := strings.HasPrefix(wire.String(), gzipFramePrefix)
		if compressed != compress {
			t.Fatalf("compress %v wrote a gzip frame: %v", compress, compressed)
		}
		r := bufio.NewReader(&wire)
		got, err := readResponse(r, MaxLineBytes, true)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("compress %v read back %d bytes, err %v; want the %d-byte line", compress, len(got), err, len(plain))
		}
		// The frame ends exactly where the next response begins.
		if next, err := readResponse(r, MaxLineBytes, true); err != nil || string(next) != `{"next":true}` {
			t.Fatalf("next frame = %q, %v", next, err)
		}
	}
}

// A line that would not shrink, and one under the threshold, go out as is.
func TestWriteResponseKeepsLinesThatDoNotShrink(t *testing.T) {
	t.Parallel()
	for name, result := range map[string]string{
		"incompressible": strconv.Quote(benchBase64(64 << 10)),
		"small":          `"` + strings.Repeat("a", GzipMinBytes/2) + `"`,
	} {
		var wire bytes.Buffer
		if err := WriteResponse(&wire, Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: json.RawMessage(result)}, true); err != nil {
			t.Fatal(err)
		}
		if wire.Bytes()[0] != '{' {
			t.Fatalf("%s result went out as %q..., want a JSON line", name, wire.Bytes()[:8])
		}
	}
}

// A small frame cannot expand past the limit a JSON line has, and a frame
// cannot claim more compressed bytes than that limit either.
func TestReadResponseBoundsGzipFrames(t *testing.T) {
	t.Parallel()
	const limit = 1 << 20
	var bomb bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&bomb, gzip.BestCompression)
	_, _ = zw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"`))
	_, _ = zw.Write(make([]byte, 64<<20))
	_ = zw.Close()
	frame := fmt.Sprintf("gzip %d\n%s", bomb.Len(), bomb.Bytes())
	if bomb.Len() > limit/8 {
		t.Fatalf("bomb is %d bytes, want it far below the limit", bomb.Len())
	}
	if _, err := readResponse(bufio.NewReader(strings.NewReader(frame)), limit, true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("64 MiB bomb in a %d-byte frame: err %v, want the line limit", bomb.Len(), err)
	}
	huge := fmt.Sprintf("gzip %d\n", limit+1)
	if _, err := readResponse(bufio.NewReader(strings.NewReader(huge)), limit, true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized frame header: err %v, want the line limit", err)
	}
	for _, header := range []string{"gzip -1\n", "gzip x\n", "gzip 99999999999999999999\n"} {
		if _, err := readResponse(bufio.NewReader(strings.NewReader(header)), limit, true); err == nil {
			t.Fatalf("header %q accepted", header)
		}
	}
	// A client that did not ask treats the frame as a malformed line.
	if line, err := readResponse(bufio.NewReader(strings.NewReader("gzip 3\nabc\n")), limit, false); err != nil || string(line) != "gzip 3" {
		t.Fatalf("client without gzip read %q, %v", line, err)
	}
}

const benchIDAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func benchID(rng *rand.Rand, prefix string) string {
	b := []byte(prefix)
	for range 10 {
		b = append(b, benchIDAlphabet[rng.IntN(len(benchIDAlphabet))])
	}
	return string(b)
}

var benchWords = strings.Fields(`fix the flaky retry in checkout make search results page faster add a
billing export for admins import repositories from a mirror report cost per
session rename the dashboard sidebar migrate the store keep channels open
review pull request checks wait for agents answer the question run tests
update docs measure latency on slow links handle a dropped connection`)

func benchText(rng *rand.Rand, words int) string {
	out := make([]string, words)
	for i := range out {
		out[i] = benchWords[rng.IntN(len(benchWords))]
	}
	return strings.Join(out, " ")
}

// benchEvents is a workspace.timeline page in the research seed's mix
// (two thirds status rereads with their wait, the rest status changes and
// agent mail) with random IDs and times, so it compresses like real
// history rather than like a repeated template.
func benchEvents(n int) []Event {
	rng := rand.New(rand.NewPCG(1, uint64(n)))
	ws := benchID(rng, "ws-")
	runs := make([]string, 40)
	for i := range runs {
		runs[i] = benchID(rng, "run-")
	}
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	out := make([]Event, n)
	for i := range out {
		at = at.Add(time.Duration(rng.IntN(90_000_000_000)))
		run := runs[rng.IntN(len(runs))]
		typ := "run.status"
		var payload any
		switch r := rng.Float64(); {
		case r < 0.66:
			payload = map[string]any{"from": "running", "to": "running", "state": "waiting-agents", "status_seq": rng.IntN(900), "cause": "turn",
				"wait": map[string]any{"kind": "condition", "detail": "pull request checks", "verified_at": at.Format(time.RFC3339Nano)}, "reread": true}
		case r < 0.85:
			payload = map[string]any{"from": "running", "to": "needs-attention", "state": "needs-you", "status_seq": rng.IntN(900), "cause": "turn", "reason": benchText(rng, 6)}
		default:
			typ = "coord.message"
			payload = map[string]any{"message_id": benchID(rng, "msg-"), "workspace_id": ws, "from_run_id": run, "to_run_id": runs[rng.IntN(len(runs))], "kind": "message"}
		}
		raw, _ := json.Marshal(payload)
		out[i] = Event{ID: benchID(rng, "evt-"), Seq: uint64(70000 + i), Time: at.Format(time.RFC3339Nano), WorkspaceID: ws, RunID: run, Type: typ, Payload: raw}
	}
	return out
}

// benchRuns is a run.list page: varied tasks and titles, random IDs.
func benchRuns(n int) []Run {
	rng := rand.New(rand.NewPCG(2, uint64(n)))
	ws := benchID(rng, "ws-")
	out := make([]Run, n)
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	for i := range out {
		at = at.Add(time.Duration(rng.IntN(3_600_000_000_000)))
		created, finished := at.Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339)
		member := benchID(rng, "mem-")
		out[i] = Run{
			ID: benchID(rng, "run-"), WorkspaceID: ws, MemberID: member, AccountMemberID: member,
			Task: benchText(rng, 8+rng.IntN(60)), Title: benchText(rng, 5), Harness: "claude", Mode: "acp", Model: "claude-opus-5-5",
			Permission: "bypass", ACP: true, Status: "completed", StatusSeq: int64(rng.IntN(900)), PendingInputs: []domain.RunInputRequest{},
			Branch: "cloudfield/" + benchID(rng, "run-"), LastCommit: fmt.Sprintf("%040x", rng.Uint64()),
			CreatedAt: created, StartedAt: &created, FinishedAt: &finished,
		}
	}
	return out
}

func benchBase64(n int) string {
	rng := rand.New(rand.NewPCG(3, uint64(n)))
	raw := make([]byte, n*3/4)
	for i := range raw {
		raw[i] = byte(rng.Uint32())
	}
	return base64.StdEncoding.EncodeToString(raw)
}

type benchPayload struct {
	name string
	line []byte
}

func benchPayloads(b *testing.B) []benchPayload {
	b.Helper()
	line := func(result any) []byte {
		raw, err := json.Marshal(result)
		if err != nil {
			b.Fatal(err)
		}
		out, _ := json.Marshal(Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: raw})
		return out
	}
	var out []benchPayload
	for _, n := range []int{10, 40, 200, 1000, 10000} {
		out = append(out, benchPayload{fmt.Sprintf("timeline-%d", n), line(WorkspaceTimelineResult{Events: benchEvents(n)})})
	}
	for _, n := range []int{10, 30, 120, 600, 3000} {
		out = append(out, benchPayload{fmt.Sprintf("runs-%d", n), line(RunListResult{Runs: benchRuns(n)})})
	}
	for _, n := range []int{16 << 10, 350 << 10, 8 << 20} {
		out = append(out, benchPayload{fmt.Sprintf("base64-%dKiB", n>>10), line(map[string]string{"content_base64": benchBase64(n)})})
	}
	return out
}

// BenchmarkGzipLevels measures each candidate level on one CPU
// (GOMAXPROCS=1): compress and decompress time per response and the ratio.
//
//	GOMAXPROCS=1 go test -run '^$' -bench GzipLevels -benchmem ./internal/protocol
func BenchmarkGzipLevels(b *testing.B) {
	for _, p := range benchPayloads(b) {
		for _, level := range []int{gzip.HuffmanOnly, gzip.BestSpeed, 3, gzip.DefaultCompression} {
			var z bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&z, level)
			b.Run(fmt.Sprintf("%s/%dB/level%d/compress", p.name, len(p.line), level), func(b *testing.B) {
				b.SetBytes(int64(len(p.line)))
				for b.Loop() {
					z.Reset()
					zw.Reset(&z)
					_, _ = zw.Write(p.line)
					_ = zw.Close()
				}
				b.ReportMetric(float64(z.Len()), "wire-B")
				b.ReportMetric(float64(len(p.line))/float64(z.Len()), "ratio")
			})
			frame := fmt.Appendf(nil, "gzip %d\n%s", z.Len(), z.Bytes())
			b.Run(fmt.Sprintf("%s/%dB/level%d/decompress", p.name, len(p.line), level), func(b *testing.B) {
				b.SetBytes(int64(len(p.line)))
				for b.Loop() {
					if _, err := readResponse(bufio.NewReader(bytes.NewReader(frame)), MaxLineBytes, true); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkWriteResponse is the server's whole cost for one response, the
// JSON marshal included, with and without compression.
func BenchmarkWriteResponse(b *testing.B) {
	for _, p := range benchPayloads(b) {
		var resp Response
		if err := json.Unmarshal(p.line, &resp); err != nil {
			b.Fatal(err)
		}
		for _, compress := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/%dB/gzip=%v", p.name, len(p.line), compress), func(b *testing.B) {
				var n countWriter
				b.SetBytes(int64(len(p.line)))
				for b.Loop() {
					n = 0
					if err := WriteResponse(&n, resp, compress); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(n), "wire-B")
			})
		}
	}
}

type countWriter int

func (c *countWriter) Write(p []byte) (int, error) { *c += countWriter(len(p)); return len(p), nil }

var _ io.Writer = (*countWriter)(nil)
