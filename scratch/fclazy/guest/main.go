// Guest /fcguest for the lazy-restore experiment: PID 1 of the VM. Scratch
// code, never merged. It listens on vsock port 5001 and answers one line per
// command line; the host keeps the expected generation of every region and
// the guest keeps none, so stale guest memory cannot hide stale pattern pages.
//
//	ping                          -> pong
//	info                          -> boot_id=.. wl=<workload counter> memfree_kb=.. memavail_kb=..
//	exec                          -> exec ok us=..        (fork /bin/sh from the root disk)
//	alloc <id> <MiB>              -> ok
//	free <id>                     -> ok
//	fill <id> <startMiB> <MiB> <gen>   -> ok us=..        (stores, never reads)
//	check <id> <startMiB> <MiB> <gen>  -> ok=<pages> zero=.. stale=.. bad=.. us=..
//	touch <id> <startMiB> <MiB>   -> ok us=.. sum=..      (reads one word per page)
//	shm <MiB>                     -> sha256=..            (writes /dev/shm/blob)
//	shmsum                        -> sha256=.. us=..
//	wl <0|1>                      -> ok                   (live alloc/free loop off/on)
//	churn <MiB>                   -> ok us=..             (map, write, unmap)
//	frag <id> <MiB>               -> ok                   (map, write, drop every other page)
//	disk <gen>                    -> ok sum=.. | err ..   (write, read back, delete, trim on /data)
//	prep                          -> ok                   (sync, drop page cache)
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

const (
	magic = 0xFC1A27C0DEC0FFEE
	mixer = 0x9E3779B97F4A7C15
	page  = 4096
	words = page / 8
	mib   = 1 << 20
)

var (
	regions = map[int][]byte{}
	wlOn    atomic.Bool
	wlCount atomic.Int64
)

func tag(id, gen, idx int) uint64 {
	return uint64(id)<<56 | uint64(gen)<<32 | uint64(idx)
}

func pageWords(b []byte, idx int) *[words]uint64 {
	return (*[words]uint64)(unsafe.Pointer(&b[idx*page]))
}

func fill(b []byte, id, start, count, gen int) {
	for idx := start * 256; idx < (start+count)*256; idx++ {
		w := pageWords(b, idx)
		t := tag(id, gen, idx)
		base := t * mixer
		w[0], w[1] = magic, t
		for k := 2; k < words; k++ {
			w[k] = base + uint64(k)
		}
	}
}

func check(b []byte, id, start, count, gen int) (ok, zero, stale, bad int) {
	for idx := start * 256; idx < (start+count)*256; idx++ {
		w := pageWords(b, idx)
		t := tag(id, gen, idx)
		base := t * mixer
		good := w[0] == magic && w[1] == t
		for k := 2; good && k < words; k++ {
			good = w[k] == base+uint64(k)
		}
		if good {
			ok++
			continue
		}
		allZero := true
		for k := 0; allZero && k < words; k++ {
			allZero = w[k] == 0
		}
		if allZero {
			zero++
			continue
		}
		// A whole page of this region and index at another generation.
		other := w[1]
		consistent := w[0] == magic && other>>56 == uint64(id) && other&0xFFFFFFFF == uint64(idx)
		for k := 2; consistent && k < words; k++ {
			consistent = w[k] == other*mixer+uint64(k)
		}
		if consistent {
			stale++
		} else {
			bad++
		}
	}
	return
}

func shmWrite(sizeMiB int) (string, error) {
	f, err := os.Create("/dev/shm/blob")
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, mib)
	x := uint64(0x2545F4914F6CDD1D)
	for i := 0; i < sizeMiB; i++ {
		w := unsafe.Slice((*uint64)(unsafe.Pointer(&buf[0])), mib/8)
		for k := range w {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			w[k] = x
		}
		if _, err = f.Write(buf); err != nil {
			return "", err
		}
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shmSum() (string, error) {
	f, err := os.Open("/dev/shm/blob")
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, mib)
	for {
		n, rerr := f.Read(buf)
		h.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func anon(sizeMiB int) ([]byte, error) {
	return unix.Mmap(-1, 0, sizeMiB*mib, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
}

func workload() {
	for {
		time.Sleep(50 * time.Millisecond)
		if !wlOn.Load() {
			continue
		}
		b, err := anon(4)
		if err != nil {
			continue
		}
		for i := 0; i < len(b); i += page {
			b[i] = byte(i >> 12)
		}
		_ = unix.Munmap(b)
		wlCount.Add(1)
	}
}

func meminfo(field string) string {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == field+":" {
			return f[1]
		}
	}
	return "?"
}

func disk(gen int) string {
	const name = "/data/blob"
	buf := make([]byte, 32*mib)
	for i := range buf {
		buf[i] = byte(i*7 + gen)
	}
	want := sha256.Sum256(buf)
	f, err := os.Create(name)
	if err != nil {
		return "err " + err.Error()
	}
	_, err = f.Write(buf)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return "err " + err.Error()
	}
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_DIRECT, 0)
	if err != nil {
		return "err " + err.Error()
	}
	back, err := anon(32)
	if err != nil {
		unix.Close(fd)
		return "err " + err.Error()
	}
	defer unix.Munmap(back)
	for off := 0; off < len(back); {
		n, rerr := unix.Read(fd, back[off:])
		if n <= 0 || rerr != nil {
			unix.Close(fd)
			return fmt.Sprintf("err read %d %v", n, rerr)
		}
		off += n
	}
	unix.Close(fd)
	if sha256.Sum256(back) != want {
		return "err read back differs"
	}
	if err = os.Remove(name); err != nil {
		return "err " + err.Error()
	}
	// FITRIM over the whole filesystem.
	d, err := unix.Open("/data", unix.O_RDONLY, 0)
	if err != nil {
		return "err " + err.Error()
	}
	defer unix.Close(d)
	// struct fstrim_range and FITRIM = _IOWR('X', 121, struct fstrim_range).
	r := struct{ Start, Len, MinLen uint64 }{Len: ^uint64(0)}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d), 0xC0185879, uintptr(unsafe.Pointer(&r))); errno != 0 {
		return "err fitrim " + errno.Error()
	}
	return fmt.Sprintf("ok sum=%s trimmed=%d", hex.EncodeToString(want[:4]), r.Len)
}

func ints(f []string) []int {
	out := make([]int, len(f))
	for i, s := range f {
		out[i], _ = strconv.Atoi(s)
	}
	return out
}

func handle(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return "err empty"
	}
	a := ints(f[1:])
	need := func(n int) bool { return len(a) >= n }
	t := time.Now()
	us := func() int64 { return time.Since(t).Microseconds() }
	switch f[0] {
	case "ping":
		return "pong"
	case "info":
		id, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		return fmt.Sprintf("boot_id=%s wl=%d memfree_kb=%s memavail_kb=%s", strings.TrimSpace(string(id)), wlCount.Load(), meminfo("MemFree"), meminfo("MemAvailable"))
	case "exec":
		out, err := exec.Command("/bin/sh", "-c", "echo ok").CombinedOutput()
		if err != nil {
			return "err " + err.Error()
		}
		return fmt.Sprintf("exec %s us=%d", strings.TrimSpace(string(out)), us())
	case "alloc":
		if !need(2) {
			break
		}
		b, err := anon(a[1])
		if err != nil {
			return "err " + err.Error()
		}
		regions[a[0]] = b
		return "ok"
	case "free":
		if !need(1) || regions[a[0]] == nil {
			break
		}
		err := unix.Munmap(regions[a[0]])
		delete(regions, a[0])
		if err != nil {
			return "err " + err.Error()
		}
		return "ok"
	case "fill", "check", "touch":
		if !need(3) || regions[a[0]] == nil || (a[1]+a[2])*mib > len(regions[a[0]]) {
			break
		}
		b := regions[a[0]]
		switch f[0] {
		case "fill":
			if !need(4) {
				return "err args"
			}
			fill(b, a[0], a[1], a[2], a[3])
			return fmt.Sprintf("ok us=%d", us())
		case "check":
			if !need(4) {
				return "err args"
			}
			ok, zero, stale, bad := check(b, a[0], a[1], a[2], a[3])
			return fmt.Sprintf("ok=%d zero=%d stale=%d bad=%d us=%d", ok, zero, stale, bad, us())
		default:
			var sum uint64
			for i := a[1] * mib; i < (a[1]+a[2])*mib; i += page {
				sum += *(*uint64)(unsafe.Pointer(&b[i+8]))
			}
			return fmt.Sprintf("ok us=%d sum=%d", us(), sum)
		}
	case "shm":
		if !need(1) {
			break
		}
		sum, err := shmWrite(a[0])
		if err != nil {
			return "err " + err.Error()
		}
		return "sha256=" + sum
	case "shmsum":
		sum, err := shmSum()
		if err != nil {
			return "err " + err.Error()
		}
		return fmt.Sprintf("sha256=%s us=%d", sum, us())
	case "wl":
		if !need(1) {
			break
		}
		wlOn.Store(a[0] != 0)
		return "ok"
	case "churn":
		if !need(1) {
			break
		}
		b, err := anon(a[0])
		if err != nil {
			return "err " + err.Error()
		}
		for i := 0; i < len(b); i += page {
			b[i] = 1
		}
		_ = unix.Munmap(b)
		return fmt.Sprintf("ok us=%d", us())
	case "frag":
		// The kept pages pin the dropped ones apart, so the guest reports
		// single free pages.
		if !need(2) {
			break
		}
		b, err := anon(a[1])
		if err != nil {
			return "err " + err.Error()
		}
		for i := 0; i < len(b); i += page {
			b[i] = 1
		}
		for i := page; i < len(b); i += 2 * page {
			_ = unix.Madvise(b[i:i+page], unix.MADV_DONTNEED)
		}
		regions[a[0]] = b
		return "ok"
	case "disk":
		if !need(1) {
			break
		}
		return disk(a[0])
	case "prep":
		unix.Sync()
		if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200); err != nil {
			return "err " + err.Error()
		}
		return "ok"
	}
	return "err args"
}

// ifUp gives an interface an IPv4 address and brings it up.
func ifUp(name string, addr, mask [4]byte) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err = ifr.SetInet4Addr(addr[:]); err == nil {
		err = unix.IoctlIfreq(fd, unix.SIOCSIFADDR, ifr)
	}
	if err == nil {
		if err = ifr.SetInet4Addr(mask[:]); err == nil {
			err = unix.IoctlIfreq(fd, unix.SIOCSIFNETMASK, ifr)
		}
	}
	if err == nil {
		if err = unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err == nil {
			ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
			err = unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
		}
	}
	return err
}

func must(what string, err error) {
	if err != nil {
		fmt.Printf("fcguest: %s: %v\n", what, err)
	}
}

func main() {
	must("proc", unix.Mount("proc", "/proc", "proc", 0, ""))
	must("sys", unix.Mount("sysfs", "/sys", "sysfs", 0, ""))
	// The kernel mounts devtmpfs itself; EBUSY is expected.
	_ = unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	_ = os.MkdirAll("/dev/shm", 0o755)
	must("shm", unix.Mount("tmpfs", "/dev/shm", "tmpfs", 0, "size=75%"))
	must("tmp", unix.Mount("tmpfs", "/tmp", "tmpfs", 0, ""))
	must("data", unix.Mount("/dev/vdb", "/data", "ext4", unix.MS_NOATIME, "discard"))
	os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	must("eth0", ifUp("eth0", [4]byte{172, 31, 77, 2}, [4]byte{255, 255, 255, 252}))
	go workload()

	l, err := vsock.Listen(5001, nil)
	if err != nil {
		fmt.Println("fcguest: vsock listen:", err)
		time.Sleep(time.Hour)
	}
	if c, derr := vsock.Dial(vsock.Host, 5000, nil); derr != nil {
		fmt.Println("fcguest: vsock dial:", derr)
	} else {
		fmt.Fprintf(c, "ready\n")
		c.Close()
	}
	fmt.Println("fcguest: ready")
	for {
		c, aerr := l.Accept()
		if aerr != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// One connection at a time: commands never run concurrently.
		rd := bufio.NewReader(c)
		for {
			line, rerr := rd.ReadString('\n')
			if rerr != nil {
				break
			}
			if _, werr := fmt.Fprintln(c, handle(line)); werr != nil {
				break
			}
		}
		c.Close()
	}
}
