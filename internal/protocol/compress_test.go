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
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGzipFrameRoundTrip(t *testing.T) {
	t.Parallel()
	result, _ := json.Marshal(WorkspaceTimelineResult{Events: benchEvents(100)})
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

func gzipped(t *testing.T, parts ...[]byte) []byte {
	t.Helper()
	var z bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	for _, p := range parts {
		_, _ = zw.Write(p)
	}
	_ = zw.Close()
	return z.Bytes()
}

func frame(body []byte) string { return fmt.Sprintf("gzip %010d\n%s", len(body), body) }

// A small frame cannot expand past the limit a JSON line has, and the
// bound holds while inflating: reading a 64 MiB bomb against a 1 MiB limit
// allocates a few MiB, not the bomb.
func TestReadResponseBoundsGzipFrames(t *testing.T) {
	const limit = 1 << 20
	bomb := gzipped(t, []byte(`{"jsonrpc":"2.0","id":1,"result":"`), make([]byte, 64<<20))
	if len(bomb) > limit/8 {
		t.Fatalf("bomb is %d bytes, want it far below the limit", len(bomb))
	}
	in := frame(bomb)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := readResponse(bufio.NewReader(strings.NewReader(in)), limit, true)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("64 MiB bomb in a %d-byte frame: err %v, want the line limit", len(bomb), err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8*limit {
		t.Fatalf("reading the bomb allocated %d bytes, want the inflation stopped near the %d-byte limit", alloc, limit)
	}
	if _, err := readResponse(bufio.NewReader(strings.NewReader(fmt.Sprintf("gzip %010d\n", limit+1))), limit, true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized frame header: err %v, want the line limit", err)
	}
}

// The header is exactly the documented 16 bytes, digits only, and the
// frame holds one gzip member and nothing after it.
func TestReadResponseRefusesMalformedGzipFrames(t *testing.T) {
	t.Parallel()
	member := gzipped(t, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	for name, in := range map[string]string{
		"short length":   "gzip 5\nabcde",
		"signed length":  "gzip +000000005\nabcde",
		"negative":       "gzip -000000005\nabcde",
		"letter":         "gzip 00000000x5\nabcde",
		"long header":    "gzip 00000000005\nabcde",
		"zero length":    "gzip 0000000000\n",
		"trailing bytes": frame(append(append([]byte{}, member...), "junk"...)),
		"two members":    frame(append(append([]byte{}, member...), member...)),
		"truncated body": fmt.Sprintf("gzip %010d\n%s", len(member)+10, member),
	} {
		if line, err := readResponse(bufio.NewReader(strings.NewReader(in)), MaxLineBytes, true); err == nil {
			t.Errorf("%s: read %q, want an error", name, line)
		}
	}
	if line, err := readResponse(bufio.NewReader(strings.NewReader(frame(member))), MaxLineBytes, true); err != nil || string(line) != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Fatalf("well-formed frame: %q, %v", line, err)
	}
	// A client that did not ask treats the frame as a malformed line.
	if line, err := readResponse(bufio.NewReader(strings.NewReader("gzip 3\nabc\n")), MaxLineBytes, false); err != nil || string(line) != "gzip 3" {
		t.Fatalf("client without gzip read %q, %v", line, err)
	}
}

type scriptedStream struct {
	io.Reader
	io.Writer
}

func (scriptedStream) Close() error { return nil }

// The server never compresses a blob method, so a client that asked still
// refuses a frame for one instead of inflating toward the blob limit.
func TestClientRefusesGzipFrameForBlobMethods(t *testing.T) {
	t.Parallel()
	member := gzipped(t, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	c := NewClient(scriptedStream{Reader: strings.NewReader(frame(member)), Writer: io.Discard})
	c.AcceptGzip()
	if err := c.Call(MethodFilesRead, nil, nil); err == nil {
		t.Fatal("files.read answered with a gzip frame was accepted")
	}
	c = NewClient(scriptedStream{Reader: strings.NewReader(frame(member)), Writer: io.Discard})
	c.AcceptGzip()
	if err := c.Call(MethodWorkspaceTimeline, nil, nil); err != nil {
		t.Fatalf("workspace.timeline answered with a gzip frame: %v", err)
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
			encoded := frame(z.Bytes())
			b.Run(fmt.Sprintf("%s/%dB/level%d/decompress", p.name, len(p.line), level), func(b *testing.B) {
				b.SetBytes(int64(len(p.line)))
				for b.Loop() {
					if _, err := readResponse(bufio.NewReader(strings.NewReader(encoded)), MaxLineBytes, true); err != nil {
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
