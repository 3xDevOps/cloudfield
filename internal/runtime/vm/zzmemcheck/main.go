// Command zzmemcheck is the guest half of the scratch incremental-save
// measurement (scratch/lazy-ch-incremental-save); never merged. It holds a
// pattern over most of the guest's RAM in which every 4 KiB page names its
// own index and the generation that last wrote it, and a live hash chain.
//
//	zzmemcheck serve
//	zzmemcheck call fill <mainMiB> <churnMiB> <gen>
//	zzmemcheck call rewrite <gen> <chunk>...
//	zzmemcheck call free
//	zzmemcheck call realloc <gen>
//	zzmemcheck call verify
//	zzmemcheck call status
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	chunk     = 1 << 20
	page      = 4096
	pageWords = page / 8
	gamma     = 0x9E3779B97F4A7C15
	spikeBase = 1 << 30
)

type state struct {
	mu          sync.Mutex
	main, churn []byte
	spike       []byte
	mainChunks  int
	churnChunks int
	mlock       string
	nonzero     int

	chainMu sync.Mutex
	counter uint64
	chain   [32]byte
}

var sock = "/run/zzmemcheck.sock"

func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	return x ^ x>>31
}

func words(b []byte) []uint64 {
	return unsafe.Slice((*uint64)(unsafe.Pointer(&b[0])), len(b)/8)
}

// fillChunks writes generation gen into mem, whose first page has global
// index firstPage.
func fillChunks(mem []byte, firstPage, gen uint64) {
	w := words(mem)
	for p := 0; p < len(mem)/page; p++ {
		idx := firstPage + uint64(p)
		pw := w[p*pageWords : (p+1)*pageWords]
		seed := mix(idx*gamma + gen)
		pw[0], pw[1] = gen, idx
		for i := 2; i < pageWords; i++ {
			pw[i] = seed + uint64(i)*gamma
		}
	}
}

// checkChunk returns the generation of a chunk's first page and how many of
// its pages are not a whole page of that generation with their own index.
func checkChunk(mem []byte, firstPage uint64) (gen uint64, bad int, why string) {
	w := words(mem)
	gen = w[0]
	for p := 0; p < len(mem)/page; p++ {
		idx := firstPage + uint64(p)
		pw := w[p*pageWords : (p+1)*pageWords]
		reason := ""
		switch {
		case pw[1] != idx:
			reason = fmt.Sprintf("page %d names index %d gen %d", idx, pw[1], pw[0])
		case pw[0] != gen:
			reason = fmt.Sprintf("page %d has gen %d in a chunk of gen %d", idx, pw[0], gen)
		default:
			seed := mix(idx*gamma + pw[0])
			for i := 2; i < pageWords; i++ {
				if pw[i] != seed+uint64(i)*gamma {
					reason = fmt.Sprintf("page %d gen %d word %d is %#x", idx, pw[0], i, pw[i])
					break
				}
			}
		}
		if reason != "" {
			if bad == 0 {
				why = reason
			}
			bad++
		}
	}
	return gen, bad, why
}

// alloc maps new anonymous memory and counts its pages that do not read as
// zero, which no page the kernel hands out may.
func (s *state) alloc(mib int) ([]byte, error) {
	mem, err := unix.Mmap(-1, 0, mib<<20, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, err
	}
	if err = unix.Mlock(mem); err != nil {
		s.mlock = "failed: " + err.Error()
	} else if s.mlock == "" {
		s.mlock = "ok"
	}
	w := words(mem)
	for p := 0; p < len(mem)/page; p++ {
		for _, v := range w[p*pageWords : (p+1)*pageWords] {
			if v != 0 {
				s.nonzero++
				break
			}
		}
	}
	return mem, nil
}

func (s *state) handle(args []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	num := func(i int) uint64 {
		v, _ := strconv.ParseUint(args[i], 10, 64)
		return v
	}
	start := time.Now()
	switch args[0] {
	case "fill":
		var err error
		s.mainChunks, s.churnChunks = int(num(1)), int(num(2))
		if s.main, err = s.alloc(s.mainChunks); err != nil {
			return "error: " + err.Error()
		}
		if s.churn, err = s.alloc(s.churnChunks); err != nil {
			return "error: " + err.Error()
		}
		fillChunks(s.main, 0, num(3))
		fillChunks(s.churn, uint64(s.mainChunks)*chunk/page, num(3))
		return fmt.Sprintf("ok nonzero=%d mlock=%q took=%s", s.nonzero, s.mlock, time.Since(start).Round(time.Millisecond))
	case "rewrite":
		gen := num(1)
		for i := 2; i < len(args); i++ {
			c := num(i)
			if c >= uint64(s.mainChunks) {
				return "error: chunk out of range"
			}
			fillChunks(s.main[c*chunk:(c+1)*chunk], c*chunk/page, gen)
		}
		return fmt.Sprintf("ok chunks=%d", len(args)-2)
	case "free":
		if err := unix.Munmap(s.churn); err != nil {
			return "error: " + err.Error()
		}
		s.churn = nil
		return "ok"
	case "realloc":
		var err error
		if s.churn, err = s.alloc(s.churnChunks); err != nil {
			return "error: " + err.Error()
		}
		fillChunks(s.churn, uint64(s.mainChunks)*chunk/page, num(1))
		return fmt.Sprintf("ok mlock=%q", s.mlock)
	case "move":
		fresh, err := s.alloc(s.churnChunks)
		if err != nil {
			return "error: " + err.Error()
		}
		fillChunks(fresh, uint64(s.mainChunks)*chunk/page, num(1))
		if err = unix.Munmap(s.churn); err != nil {
			return "error: " + err.Error()
		}
		s.churn = fresh
		return fmt.Sprintf("ok nonzero=%d took=%s", s.nonzero, time.Since(start).Round(time.Millisecond))
	case "transient":
		mem, err := s.alloc(int(num(1)))
		if err != nil {
			return "error: " + err.Error()
		}
		fillChunks(mem, spikeBase*2, 7)
		if err = unix.Munmap(mem); err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf("ok nonzero=%d took=%s", s.nonzero, time.Since(start).Round(time.Millisecond))
	case "spike":
		var err error
		if s.spike, err = s.alloc(int(num(1))); err != nil {
			return "error: " + err.Error()
		}
		fillChunks(s.spike, spikeBase, 99)
		return fmt.Sprintf("ok nonzero=%d took=%s", s.nonzero, time.Since(start).Round(time.Millisecond))
	case "unspike":
		bad := 0
		for c := 0; c < len(s.spike)/chunk; c++ {
			gen, n, _ := checkChunk(s.spike[c*chunk:(c+1)*chunk], spikeBase+uint64(c)*chunk/page)
			if gen != 99 && n == 0 {
				n = chunk / page
			}
			bad += n
		}
		if err := unix.Munmap(s.spike); err != nil {
			return "error: " + err.Error()
		}
		s.spike = nil
		return fmt.Sprintf("ok bad=%d took=%s", bad, time.Since(start).Round(time.Millisecond))
	case "verify":
		var b strings.Builder
		bad, first := 0, ""
		var runGen uint64
		runLen := 0
		flush := func() {
			if runLen > 0 {
				fmt.Fprintf(&b, "%d*%d,", runGen, runLen)
			}
		}
		check := func(mem []byte, firstChunk int) {
			for c := 0; c < len(mem)/chunk; c++ {
				gen, n, why := checkChunk(mem[c*chunk:(c+1)*chunk], uint64(firstChunk+c)*chunk/page)
				if n > 0 && first == "" {
					first = why
				}
				bad += n
				if gen != runGen || runLen == 0 {
					flush()
					runGen, runLen = gen, 0
				}
				runLen++
			}
		}
		check(s.main, 0)
		if s.churn != nil {
			check(s.churn, s.mainChunks)
		}
		flush()
		return fmt.Sprintf("bad=%d took=%s first=%q gens=%s", bad, time.Since(start).Round(time.Millisecond), first, b.String())
	case "status":
		s.chainMu.Lock()
		defer s.chainMu.Unlock()
		return fmt.Sprintf("pid=%d counter=%d chain=%s", os.Getpid(), s.counter, hex.EncodeToString(s.chain[:]))
	}
	return "error: unknown command"
}

func serve() error {
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	s := &state{chain: sha256.Sum256([]byte("zzmemcheck"))}
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)
			s.chainMu.Lock()
			s.chain = sha256.Sum256(s.chain[:])
			s.counter++
			s.chainMu.Unlock()
		}
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		line, _ := bufio.NewReaderSize(c, 1<<20).ReadString('\n')
		if args := strings.Fields(line); len(args) > 0 {
			_, _ = io.WriteString(c, s.handle(args)+"\n")
		}
		_ = c.Close()
	}
}

func call(args []string) error {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(c, strings.Join(args, " ")+"\n"); err != nil {
		return err
	}
	_, err = io.Copy(os.Stdout, c)
	return err
}

func main() {
	var err error
	if v := os.Getenv("ZZ_SOCK"); v != "" {
		sock = v
	}
	switch {
	case len(os.Args) == 2 && os.Args[1] == "serve":
		err = serve()
	case len(os.Args) > 2 && os.Args[1] == "call":
		err = call(os.Args[2:])
	default:
		err = fmt.Errorf("usage: zzmemcheck serve | call <command>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zzmemcheck:", err)
		os.Exit(1)
	}
}
