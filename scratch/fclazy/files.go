package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

const (
	magic    = 0xFC1A27C0DEC0FFEE
	mixer    = 0x9E3779B97F4A7C15
	pageSize = 4096
	stableID = 1
	heapID   = 2
)

// expect is what the host knows the guest holds: the generation of every MiB
// of each pattern region.
type expect struct {
	gens   map[int][]int
	shmMiB int
	shmSHA string
	bootID string
}

func (e *expect) clone() *expect {
	c := *e
	c.gens = map[int][]int{}
	for id, g := range e.gens {
		c.gens[id] = append([]int(nil), g...)
	}
	return &c
}

func (e *expect) set(id, start, count, gen int) {
	for i := start; i < start+count; i++ {
		e.gens[id][i] = gen
	}
}

func (e *expect) pages() int {
	n := 0
	for _, g := range e.gens {
		n += len(g) * 256
	}
	return n
}

type compared struct {
	pages int
	// differ = fullZero + mergedZero + other, in pages. fullZero: the Full
	// snapshot holds zeroes and the merged file holds data, which is what a
	// page the balloon discarded and the guest has not reused looks like.
	differ, fullZero, mergedZero, other int
	// Pattern pages found in the merged file at the expected generation,
	// those missing, and whole pattern pages at another generation.
	found, missing, staleCopies int
	// offset of each stable page in the merged file, -1 when not found.
	stableOff []int64
	took      time.Duration
}

func isZero(b []byte) bool {
	for len(b) >= 8 {
		if binary.LittleEndian.Uint64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	return true
}

// compare reads the merged memory file page by page, against full when it is
// set, and finds the pattern pages e expects.
func compare(merged, full string, e *expect) (compared, error) {
	var c compared
	t := time.Now()
	mf, err := os.Open(merged)
	if err != nil {
		return c, err
	}
	defer mf.Close()
	var ff *os.File
	if full != "" {
		if ff, err = os.Open(full); err != nil {
			return c, err
		}
		defer ff.Close()
	}
	seen := map[int][]bool{}
	if e != nil {
		for id, g := range e.gens {
			seen[id] = make([]bool, len(g)*256)
		}
		c.stableOff = make([]int64, len(e.gens[stableID])*256)
		for i := range c.stableOff {
			c.stableOff[i] = -1
		}
	}
	const chunk = 4 << 20
	mb, fb := make([]byte, chunk), make([]byte, chunk)
	for off := int64(0); ; off += chunk {
		n, rerr := io.ReadFull(mf, mb)
		if n == 0 {
			break
		}
		if n%pageSize != 0 {
			return c, fmt.Errorf("%s: size is not a multiple of the page size", merged)
		}
		same := false
		if ff != nil {
			if fn, _ := io.ReadFull(ff, fb); fn != n {
				return c, fmt.Errorf("%s and %s differ in size", merged, full)
			}
			same = bytes.Equal(mb[:n], fb[:n])
		}
		for p := 0; p < n; p += pageSize {
			c.pages++
			mp := mb[p : p+pageSize]
			if ff != nil && !same && !bytes.Equal(mp, fb[p:p+pageSize]) {
				c.differ++
				switch {
				case isZero(fb[p : p+pageSize]):
					c.fullZero++
				case isZero(mp):
					c.mergedZero++
				default:
					c.other++
				}
			}
			if e == nil || binary.LittleEndian.Uint64(mp) != magic {
				continue
			}
			tag := binary.LittleEndian.Uint64(mp[8:])
			if binary.LittleEndian.Uint64(mp[16:]) != tag*mixer+2 || binary.LittleEndian.Uint64(mp[pageSize-8:]) != tag*mixer+511 {
				continue
			}
			id, gen, idx := int(tag>>56), int(tag>>32&0xFFFFFF), int(tag&0xFFFFFFFF)
			if s := seen[id]; s == nil || idx >= len(s) {
				continue
			}
			if gen != e.gens[id][idx/256] {
				c.staleCopies++
				continue
			}
			if !seen[id][idx] {
				seen[id][idx] = true
				c.found++
			}
			if id == stableID {
				c.stableOff[idx] = off + int64(p)
			}
		}
		if rerr != nil {
			break
		}
	}
	if e != nil {
		c.missing = e.pages() - c.found
	}
	c.took = time.Since(t)
	return c, nil
}

func (c compared) String() string {
	return fmt.Sprintf("pages=%d differ=%d differ_full_zero=%d differ_merged_zero=%d differ_other=%d pattern_found=%d pattern_missing=%d pattern_stale_copies=%d compare_ms=%s",
		c.pages, c.differ, c.fullZero, c.mergedZero, c.other, c.found, c.missing, c.staleCopies, ms(c.took))
}

// sizeMiB returns a file's apparent and allocated size.
func sizeMiB(path string) (apparent, allocated int64) {
	var st unix.Stat_t
	if unix.Stat(path, &st) == nil {
		return st.Size >> 20, st.Blocks * 512 >> 20
	}
	return -1, -1
}

// setCache puts files in a page cache state: cold (written out and evicted)
// or warm (whatever is cached stays and the rest of the data is read in).
// Reads skip holes: reading one would put zero pages in the cache.
func setCache(state string, files ...string) error {
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		if state == "cold" {
			if err = f.Sync(); err == nil {
				err = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
			}
		} else {
			for off := int64(0); err == nil; {
				var end int64
				if off, err = unix.Seek(int(f.Fd()), off, unix.SEEK_DATA); err != nil {
					// ENXIO: no data after off.
					err = nil
					break
				}
				if end, err = unix.Seek(int(f.Fd()), off, unix.SEEK_HOLE); err == nil {
					_, err = io.Copy(io.Discard, io.NewSectionReader(f, off, end-off))
					off = end
				}
			}
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func fsync(path string) (time.Duration, error) {
	t := time.Now()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	err = f.Sync()
	return time.Since(t), err
}

func sh(name string, args ...string) (time.Duration, error) {
	t := time.Now()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return time.Since(t), fmt.Errorf("%s %v: %w: %s", name, args, err, bytes.TrimSpace(out))
	}
	return time.Since(t), nil
}

// copyFile clones src: a reflink where the filesystem has them, else a sparse
// copy.
func copyFile(src, dst string) (time.Duration, error) {
	_ = os.Remove(dst)
	return sh("cp", "--reflink=auto", "--sparse=always", src, dst)
}

// rebase merges a diff memory file onto base with Firecracker's tool and
// writes base out.
func rebase(base, diff string) (merge, sync time.Duration, err error) {
	if merge, err = sh(*editor, "edit-memory", "rebase", "--memory-path", base, "--diff-path", diff); err != nil {
		return
	}
	sync, err = fsync(base)
	return
}
