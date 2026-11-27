#!/bin/sh
# Scratch measurement (scratch/lazy-ch-incremental-save); never merged.
# Usage: sudo zz-lazy-incremental.sh <xfs|btrfs|ext4> <MiB> <kernel>
set -eu
fs=$1 mib=$2 kernel=$3

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
echo "ZZ host uncap=${LAZY_UNCAP:-0}"
echo "ZZ host fs=$fs data=$work/data $(findmnt -no SOURCE,FSTYPE,OPTIONS -T "$work/data") dump_fs=$(findmnt -no FSTYPE -T "$work/dump")"
[ "$fs" != xfs ] || xfs_info "$work/data"

dir=$(mktemp -d /tmp/aether-kvm.XXXXXX)
CGO_ENABLED=0 go build -o "$dir/aether-server" ./cmd/aether-server
CGO_ENABLED=0 go build -o "$dir/zzmemcheck" ./internal/runtime/vm/zzmemcheck
status=0
CGO_ENABLED=0 TMPDIR="$dir" AETHER_VM_TEST_KERNEL="$kernel" AETHER_VM_TEST_SERVER="$dir/aether-server" \
	LAZY_DIR="$work/data" LAZY_FS="$fs" LAZY_MIB="$mib" LAZY_DUMP_DIR="$work/dump" LAZY_MEMCHECK="$dir/zzmemcheck" \
	LAZY_DEV_STAT="/sys/class/block/$(basename "$(readlink -f "$src")")/stat" LAZY_CYCLES="${LAZY_CYCLES:-12}" LAZY_UNCAP="${LAZY_UNCAP:-0}" \
	go test -count=1 -p 1 -timeout=100m -tags kvm -v -run '^TestKVMZZIncrementalSave' ./internal/runtime/vm || status=$?
df -hT "$work/data"
dmesg | grep -iE 'xfs|btrfs|ext4|oom|killed process|I/O error' | tail -30 || true
exit $status
