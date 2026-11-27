//go:build kvm

package vm

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	memcheckSock  = "/run/memcheck.sock"
	memcheckPage  = 4096
	memcheckChunk = 1 << 20
	// memcheckChurn is what the live loop takes and frees each round. It
	// stays under reportSettled, so a save never waits for its pages.
	memcheckChurn = 8 << 20
	memcheckGamma = 0x9e3779b97f4a7c15
)

// TestKVMMemcheckNow runs inside a guest, from the test binary. With a
// number in AETHER_VM_TEST_MEMCHECK it holds that many MiB of pattern and
// answers on memcheckSock; with anything else it sends that line there and
// prints the answer.
func TestKVMMemcheckNow(t *testing.T) {
	mode := os.Getenv("AETHER_VM_TEST_MEMCHECK")
	if mode == "" {
		t.Skip("runs inside a guest only")
	}
	if mib, err := strconv.Atoi(mode); err == nil {
		t.Fatal(memcheckServe(mib))
	}
	conn, err := net.Dial("unix", memcheckSock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = fmt.Fprintln(conn, mode); err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(os.Stdout, conn); err != nil {
		t.Fatal(err)
	}
}

func memcheckMix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ x>>31
}

// memcheckFill writes generation gen into mem, whose first page has index
// first: each page holds the generation, its index, then words no other
// page or generation has.
func memcheckFill(mem []byte, first, gen uint64) {
	for p := 0; p < len(mem); p += memcheckPage {
		index := first + uint64(p/memcheckPage)
		seed := memcheckMix(index*memcheckGamma + gen)
		binary.LittleEndian.PutUint64(mem[p:], gen)
		binary.LittleEndian.PutUint64(mem[p+8:], index)
		for i := 2; i < memcheckPage/8; i++ {
			binary.LittleEndian.PutUint64(mem[p+i*8:], seed+uint64(i)*memcheckGamma)
		}
	}
}

// memcheckRead returns the generation mem's first page names and how many
// of its pages are not whole pages of that generation with their own index.
func memcheckRead(mem []byte, first uint64) (gen uint64, bad int) {
	gen = binary.LittleEndian.Uint64(mem)
	for p := 0; p < len(mem); p += memcheckPage {
		index := first + uint64(p/memcheckPage)
		seed := memcheckMix(index*memcheckGamma + gen)
		whole := binary.LittleEndian.Uint64(mem[p:]) == gen && binary.LittleEndian.Uint64(mem[p+8:]) == index
		for i := 2; whole && i < memcheckPage/8; i++ {
			whole = binary.LittleEndian.Uint64(mem[p+i*8:]) == seed+uint64(i)*memcheckGamma
		}
		if !whole {
			bad++
		}
	}
	return gen, bad
}

func memcheckMap(size int) ([]byte, error) {
	return unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
}

// memcheckServe holds mib MiB of locked memory, every page stamped with
// its index and generation 1, beside a loop that takes, stamps, reads back
// and frees memcheckChurn. Each connection sends one line:
//
//	rewrite <gen> <rem>  stamp every chunk whose index is rem modulo 3
//	verify               read every page; report each chunk's generation
//	                     in runs of gen*chunks, and the pages that are bad
//
// Every answer starts with the process, the boot and the loop's rounds.
func memcheckServe(mib int) error {
	mem, err := memcheckMap(mib * memcheckChunk)
	if err != nil {
		return err
	}
	// Locked, or the guest would swap the pattern out and a wake would
	// read it from the swap disk.
	if err = unix.Mlock(mem); err != nil {
		return err
	}
	memcheckFill(mem, 0, 1)
	var rounds, torn atomic.Uint64
	go func() {
		for round := uint64(1); ; round++ {
			live, mapErr := memcheckMap(memcheckChurn)
			if mapErr != nil {
				torn.Add(1)
				continue
			}
			memcheckFill(live, 0, round)
			if gen, bad := memcheckRead(live, 0); gen != round || bad != 0 {
				torn.Add(1)
			}
			_ = unix.Munmap(live)
			rounds.Add(1)
			time.Sleep(50 * time.Millisecond)
		}
	}()
	_ = os.Remove(memcheckSock)
	l, err := net.Listen("unix", memcheckSock)
	if err != nil {
		return err
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		line, _ := bufio.NewReader(conn).ReadString('\n')
		// Read for every answer: a copy from before a save would vouch for
		// a kernel that is not the one answering.
		boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return err
		}
		stat, err := os.ReadFile("/proc/self/stat")
		if err != nil {
			return err
		}
		// Field 22, the start time, counted from after the command's ")".
		started := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))[19]
		answer := fmt.Sprintf("MEMCHECK id=%d/%s/%s rounds=%d torn=%d", os.Getpid(), started, strings.TrimSpace(string(boot)), rounds.Load(), torn.Load())
		switch args := strings.Fields(line); {
		case len(args) == 3 && args[0] == "rewrite":
			gen, _ := strconv.ParseUint(args[1], 10, 64)
			rem, _ := strconv.Atoi(args[2])
			for c := rem; c < mib; c += 3 {
				memcheckFill(mem[c*memcheckChunk:(c+1)*memcheckChunk], uint64(c)*memcheckChunk/memcheckPage, gen)
			}
		case len(args) == 1 && args[0] == "verify":
			var runs strings.Builder
			var bad, n int
			var run uint64
			for c := range mib {
				gen, chunkBad := memcheckRead(mem[c*memcheckChunk:(c+1)*memcheckChunk], uint64(c)*memcheckChunk/memcheckPage)
				bad += chunkBad
				if n > 0 && gen != run {
					fmt.Fprintf(&runs, "%d*%d,", run, n)
					n = 0
				}
				run, n = gen, n+1
			}
			answer += fmt.Sprintf(" bad=%d gens=%s%d*%d", bad, runs.String(), run, n)
		}
		_, _ = fmt.Fprintln(conn, answer)
		_ = conn.Close()
	}
}
