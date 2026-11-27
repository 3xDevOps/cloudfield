#!/bin/sh
# Scratch measurement (scratch/lazy-file-bound); never merged.
# Usage: sudo zz-lazy-file-bound.sh <xfs|btrfs|ext4> <MiB> <kernel> <A|B|C|D|cost> <cycles>
set -eu
fs=$1 mib=$2 kernel=$3 arm=$4 cycles=$5

base=/var/tmp
if mountpoint -q /mnt && [ "$(df --output=avail /mnt | tail -1)" -gt "$(df --output=avail /var/tmp | tail -1)" ]; then
	base=/mnt
fi
if [ "$(df --output=avail "$base" | tail -1)" -lt 47185920 ]; then
	rm -rf /usr/local/lib/android /opt/hostedtoolcache/CodeQL /usr/share/dotnet /opt/ghc
fi
echo "ZZ host base=$base runner=${RUNNER_NAME:-?} cpu=$(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | tr -s " ") kernel=$(uname -r) cpus=$(nproc) $(grep -E '^(MemTotal|MemAvailable):' /proc/meminfo | tr -s ' \n' ' ')"
df -hT "$base"
work=$base/lazy
mkdir -p "$work/data" "$work/dump"
case $fs in
xfs | btrfs)
	truncate -s 34G "$work/img"
	dev=$(losetup --find --show --direct-io=on "$work/img")
	if [ "$fs" = xfs ]; then
		mkfs.xfs -q -m reflink=1,crc=1 "$dev"
	else
		mkfs.btrfs -q "$dev"
	fi
	mount "$dev" "$work/data"
	;;
ext4) ;;
esac
src=$(findmnt -no SOURCE -T "$work/data")
echo "ZZ host arm=$arm cycles=$cycles uncap=${LAZY_UNCAP:-1} fallocate=$(fallocate --version)"
echo "ZZ host fs=$fs data=$work/data $(findmnt -no SOURCE,FSTYPE,OPTIONS -T "$work/data") dump_fs=$(findmnt -no FSTYPE -T "$work/dump")"
[ "$fs" != xfs ] || xfs_info "$work/data"

dir=$(mktemp -d /tmp/aether-kvm.XXXXXX)
CGO_ENABLED=0 go build -o "$dir/aether-server" ./cmd/aether-server
CGO_ENABLED=0 go build -o "$dir/zzmemcheck" ./internal/runtime/vm/zzmemcheck
if [ "$arm" = cost ]; then
	tar -C "$(go env GOROOT)" --exclude=./test --exclude=./api --exclude=./doc -cf - . | gzip -1 >"$dir/goroot.tgz"
	ls -l "$dir/goroot.tgz"
fi
status=0
CGO_ENABLED=0 TMPDIR="$dir" AETHER_VM_TEST_KERNEL="$kernel" AETHER_VM_TEST_SERVER="$dir/aether-server" \
	LAZY_DIR="$work/data" LAZY_FS="$fs" LAZY_MIB="$mib" LAZY_DUMP_DIR="$work/dump" LAZY_MEMCHECK="$dir/zzmemcheck" \
	LAZY_DEV_STAT="/sys/class/block/$(basename "$(readlink -f "$src")")/stat" LAZY_CYCLES="$cycles" LAZY_UNCAP="${LAZY_UNCAP:-1}" \
	LAZY_ARM="$arm" LAZY_GOROOT="$dir/goroot.tgz" \
	go test -count=1 -p 1 -timeout=110m -tags kvm -v -run '^TestKVMZZ(FileBound|InitOnFreeCost)' ./internal/runtime/vm || status=$?
df -hT "$work/data"
dmesg | grep -iE 'xfs|btrfs|ext4|oom|killed process|I/O error' | tail -30 || true
exit $status
