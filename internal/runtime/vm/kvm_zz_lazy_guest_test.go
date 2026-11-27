//go:build kvm

package vm

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Scratch code for the lazy-restore experiment, never merged. These are the
// programs the experiment runs inside the guest, from the test binary.

const (
	lazyPage        = 4096
	lazyPatternSock = "/var/tmp/lazy-pattern.sock"
	lazyWorkStatus  = "/var/tmp/lazy-work.status"
)

// TestKVMZZLazyGuestNow is what TestKVMZZLazy runs inside the guest.
func TestKVMZZLazyGuestNow(t *testing.T) {
	mode := os.Getenv("AETHER_LAZY_GUEST")
	if mode == "" {
		t.Skip("runs inside a guest only")
	}
	mib, _ := strconv.Atoi(os.Getenv("AETHER_LAZY_MIB"))
	var err error
	switch mode {
	case "pattern":
		err = lazyPatternServe(mib)
	case "work":
		err = lazyWorkLoop(mib)
	case "verify":
		err = lazyPatternAsk()
	}
	if err != nil {
		fmt.Println("LAZYGUEST-ERROR", err)
		os.Exit(3)
	}
}

func lazyMix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func lazyHash(key, index, gen uint64) uint64 {
	return lazyMix(lazyMix(key^index*0x9e3779b97f4a7c15) ^ gen*0xc2b2ae3d27d4eb4f)
}

// lazyStamp writes page index's stamp: the index, the generation and their
// keyed hash at the start, the hash again in the middle and inverted at
// the end.
func lazyStamp(page []byte, key, index, gen uint64) {
	h := lazyHash(key, index, gen)
	binary.LittleEndian.PutUint64(page[0:], index)
	binary.LittleEndian.PutUint64(page[8:], gen)
	binary.LittleEndian.PutUint64(page[16:], h)
	binary.LittleEndian.PutUint64(page[2048:], h)
	binary.LittleEndian.PutUint64(page[lazyPage-8:], ^h)
}

// lazyCheck is "" for a page that holds index's stamp of generation gen,
// else zero, stale (an older generation's whole stamp) or other.
func lazyCheck(page []byte, key, index, gen uint64) (string, uint64) {
	i := binary.LittleEndian.Uint64(page[0:])
	g := binary.LittleEndian.Uint64(page[8:])
	h := binary.LittleEndian.Uint64(page[16:])
	mid := binary.LittleEndian.Uint64(page[2048:])
	end := binary.LittleEndian.Uint64(page[lazyPage-8:])
	whole := i == index && h == lazyHash(key, index, g) && mid == h && end == ^h
	if whole && g == gen {
		return "", g
	}
	if whole && g < gen {
		return "stale", g
	}
	for _, b := range page {
		if b != 0 {
			return "other", g
		}
	}
	return "zero", g
}

// lazyGPA is the guest physical address behind a virtual address of this
// process, from /proc/self/pagemap.
func lazyGPA(pagemap *os.File, addr uintptr) string {
	var entry [8]byte
	if _, err := pagemap.ReadAt(entry[:], int64(addr/lazyPage)*8); err != nil {
		return "nopagemap"
	}
	v := binary.LittleEndian.Uint64(entry[:])
	if v>>63 == 0 {
		return "notpresent"
	}
	return fmt.Sprintf("%#x", (v&(1<<55-1))*lazyPage)
}

// lazyPatternServe holds mib MiB of anonymous pages, each stamped, and on
// every "verify" checks them all, then moves a random third to the next
// generation.
func lazyPatternServe(mib int) error {
	n := mib << 20 / lazyPage
	mem, err := unix.Mmap(-1, 0, n*lazyPage, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return err
	}
	var seed [8]byte
	if _, err = rand.Read(seed[:]); err != nil {
		return err
	}
	key := binary.LittleEndian.Uint64(seed[:])
	gens := make([]uint32, n)
	for i := range n {
		gens[i] = 1
		lazyStamp(mem[i*lazyPage:(i+1)*lazyPage], key, uint64(i), 1)
	}
	pagemap, err := os.Open("/proc/self/pagemap")
	if err != nil {
		return err
	}
	_ = os.Remove(lazyPatternSock)
	l, err := net.Listen("unix", lazyPatternSock)
	if err != nil {
		return err
	}
	base := uintptr(unsafe.Pointer(&mem[0]))
	round := 0
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if strings.TrimSpace(line) != "verify" {
			_, _ = fmt.Fprintf(conn, "PATTERN pid=%d pages=%d\n", os.Getpid(), n)
			_ = conn.Close()
			continue
		}
		round++
		start := time.Now()
		counts := map[string]int{}
		var bad []string
		for i := range n {
			kind, found := lazyCheck(mem[i*lazyPage:(i+1)*lazyPage], key, uint64(i), uint64(gens[i]))
			if kind == "" {
				continue
			}
			counts[kind]++
			if len(bad) < 24 {
				bad = append(bad, fmt.Sprintf("%s:%s:i%d:g%d/%d", lazyGPA(pagemap, base+uintptr(i*lazyPage)), kind, i, found, gens[i]))
			}
		}
		verified := time.Since(start)
		bumped := 0
		for i := range n {
			if mrand.IntN(3) != 0 {
				continue
			}
			gens[i]++
			lazyStamp(mem[i*lazyPage:(i+1)*lazyPage], key, uint64(i), uint64(gens[i]))
			bumped++
		}
		_, _ = fmt.Fprintf(conn, "VERIFY round=%d pages=%d zero=%d stale=%d other=%d bumped=%d verify_ms=%d bump_ms=%d bad=[%s]\n",
			round, n, counts["zero"], counts["stale"], counts["other"], bumped, verified.Milliseconds(), (time.Since(start) - verified).Milliseconds(), strings.Join(bad, " "))
		_ = conn.Close()
	}
}

func lazyPatternAsk() error {
	conn, err := net.Dial("unix", lazyPatternSock)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = fmt.Fprintln(conn, os.Getenv("AETHER_LAZY_ASK")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Print(line)
	return nil
}

func lazyTreeHash(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		one := sha256.Sum256(data)
		sum.Write([]byte(name))
		sum.Write(one[:])
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// lazyWorkLoop is the live workload: it builds a file tree and hashes it,
// takes and frees mib MiB, and hashes the tree again, over and over. The
// status file holds how many rounds ended and how many read a tree back
// changed.
func lazyWorkLoop(mib int) error {
	const dir = "/var/tmp/lazy-tree"
	bad := 0
	buf := make([]byte, 256<<10)
	for iter := 1; ; iter++ {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for j := range 64 {
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d", j)), buf, 0o644); err != nil {
				return err
			}
		}
		before, err := lazyTreeHash(dir)
		if err != nil {
			return err
		}
		mem, err := unix.Mmap(-1, 0, mib<<20, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
		if err != nil {
			return err
		}
		for off := 0; off < len(mem); off += lazyPage {
			mem[off] = byte(iter) | 1
		}
		if err = unix.Munmap(mem); err != nil {
			return err
		}
		after, err := lazyTreeHash(dir)
		if err != nil {
			return err
		}
		if before != after {
			bad++
		}
		if err = os.WriteFile(lazyWorkStatus+".tmp", fmt.Appendf(nil, "%d %d\n", iter, bad), 0o644); err != nil {
			return err
		}
		if err = os.Rename(lazyWorkStatus+".tmp", lazyWorkStatus); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}
