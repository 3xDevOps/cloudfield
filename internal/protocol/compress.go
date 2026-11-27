package protocol

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// EncodingGzip is the one Request.AcceptEncoding value: the caller reads a
// gzip frame (docs/local-gateway.md#compressed-responses) in place of a
// response line.
const EncodingGzip = "gzip"

// GzipMinBytes is the smallest response line WriteResponse compresses.
const GzipMinBytes = 8 << 10

// GzipMethods are the methods whose result may go out as a gzip frame; a
// client sends accept_encoding only for them. The rule: one compression
// context per access domain. A response that can span workspaces (run.list,
// changes.list and other member-wide reads) must not be added, or text one
// workspace's member writes would be compressed beside another workspace's,
// and the frame's size would tell them about it;
// docs/local-gateway.md#compressed-responses.
var GzipMethods = map[string]bool{
	MethodWorkspaceTimeline: true,
}

const (
	gzipFramePrefix = "gzip "
	// gzipHeaderBytes is the "gzip <n>" line: the prefix, n as ten
	// zero-padded digits, and the newline.
	gzipHeaderBytes = len(gzipFramePrefix) + 11
)

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return w
}}

// WriteResponse writes resp as one frame: a gzip frame when compress is set,
// the line is between GzipMinBytes and MaxLineBytes and compressing shrinks
// it, otherwise the newline-terminated JSON line. A line over MaxLineBytes
// goes out as is, so the client refuses it before inflating anything.
func WriteResponse(w io.Writer, resp Response, compress bool) error {
	line, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if compress && len(line) >= GzipMinBytes && len(line) <= MaxLineBytes {
		frame := bytes.NewBuffer(make([]byte, gzipHeaderBytes, gzipHeaderBytes+len(line)/4))
		zw := gzipWriters.Get().(*gzip.Writer)
		zw.Reset(frame)
		_, _ = zw.Write(line)
		_ = zw.Close()
		zw.Reset(io.Discard)
		gzipWriters.Put(zw)
		if frame.Len() < len(line) {
			out := frame.Bytes()
			copy(out, fmt.Sprintf("%s%010d\n", gzipFramePrefix, len(out)-gzipHeaderBytes))
			_, err = w.Write(out)
			return err
		}
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

// readResponse reads one response frame. Only a caller that asked for gzip
// accepts a gzip frame; both its compressed and decompressed sizes are
// held to maxBytes, the limit an uncompressed line has, and the body is
// read as it arrives rather than allocated at its declared size.
func readResponse(r *bufio.Reader, maxBytes int, gzipOK bool) ([]byte, error) {
	if !gzipOK {
		return readLine(r, maxBytes)
	}
	if head, _ := r.Peek(len(gzipFramePrefix)); string(head) != gzipFramePrefix {
		return readLine(r, maxBytes)
	}
	header, err := readLine(r, gzipHeaderBytes)
	if err != nil {
		return nil, err
	}
	digits := header[len(gzipFramePrefix):]
	n := 0
	for _, d := range digits {
		if d < '0' || d > '9' {
			n = -1
			break
		}
		n = n*10 + int(d-'0')
	}
	if len(header) != gzipHeaderBytes-1 || n <= 0 {
		return nil, fmt.Errorf("protocol: bad gzip frame header %q", header)
	}
	if n > maxBytes {
		return nil, fmt.Errorf("protocol: line exceeds %d bytes", maxBytes)
	}
	var body bytes.Buffer
	if _, err = io.CopyN(&body, r, int64(n)); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(&body)
	if err != nil {
		return nil, fmt.Errorf("protocol: gzip frame: %w", err)
	}
	zr.Multistream(false)
	line, err := io.ReadAll(io.LimitReader(zr, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("protocol: gzip frame: %w", err)
	}
	if len(line) > maxBytes {
		return nil, fmt.Errorf("protocol: line exceeds %d bytes", maxBytes)
	}
	if body.Len() != 0 {
		return nil, fmt.Errorf("protocol: gzip frame: %d bytes after the gzip member", body.Len())
	}
	return line, nil
}
