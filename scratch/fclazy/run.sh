#!/bin/bash
# Firecracker lazy restore and diff snapshots on real KVM. Run as root from
# the repository root after `aether-server vm install`. Scratch, never merged.
#   FCLAZY_MEM (2048), FCLAZY_FS (ext4|xfs), FCLAZY_CYCLES, FCLAZY_MINCORE_CYCLES,
#   FCLAZY_CHURN_MIN, FCLAZY_N, FCLAZY_SECTIONS
set -uo pipefail

FC_VERSION=v1.18.0
MEM=${FCLAZY_MEM:-2048}
FS=${FCLAZY_FS:-ext4}
IMAGE=${FCLAZY_IMAGE:-ghcr.io/3xdevops/aether-standard:latest}
repo=$PWD
kernel=$repo/dist/aether-vmlinux-x86_64
tap=fctap0

top=/tmp/fclazy
if [ "$(df --output=avail /mnt 2>/dev/null | tail -1)" -gt "$(df --output=avail /tmp | tail -1)" ]; then top=/mnt/fclazy; fi
umount "$top/xfs" 2>/dev/null
rm -rf "$top"; mkdir -p "$top/dl" "$top/bin"

echo "FCLAZY-ENV top=$top mem=$MEM fs=$FS"
echo "FCLAZY-ENV cpu=$(lscpu | sed -n 's/^Model name: *//p')"
echo "FCLAZY-ENV nproc=$(nproc) mem_mib=$(free -m | awk '/^Mem:/{print $2}') swap_mib=$(free -m | awk '/^Swap:/{print $2}')"
echo "FCLAZY-ENV kernel=$(uname -r) virt=$(systemd-detect-virt)"
echo "FCLAZY-ENV thp=$(cat /sys/kernel/mm/transparent_hugepage/enabled) max_map_count=$(cat /proc/sys/vm/max_map_count) overcommit=$(cat /proc/sys/vm/overcommit_memory)"
echo "FCLAZY-ENV fs_top=$(df -T "$top" | tail -1 | awk '{print $2, "avail_gib=" int($5/1048576)}')"
echo "FCLAZY-ENV ch=$(cloud-hypervisor --version)"
echo "FCLAZY-ENV repo_kernel=$(strings "$kernel" | grep -m1 '^Linux version')"
# The mincore flavour of a diff snapshot is documented to need swap off.
swapoff -a
echo "FCLAZY-ENV swap_after_swapoff_mib=$(free -m | awk '/^Swap:/{print $2}')"

# Firecracker release, verified against the sha256 file published with it.
(
	cd "$top/dl" &&
	curl -fsSLO "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-x86_64.tgz" &&
	curl -fsSLO "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-x86_64.tgz.sha256.txt" &&
	sha256sum -c "firecracker-$FC_VERSION-x86_64.tgz.sha256.txt" &&
	tar -xzf "firecracker-$FC_VERSION-x86_64.tgz" &&
	cp "release-$FC_VERSION-x86_64/firecracker-$FC_VERSION-x86_64" "$top/bin/firecracker" &&
	cp "release-$FC_VERSION-x86_64/snapshot-editor-$FC_VERSION-x86_64" "$top/bin/snapshot-editor"
) || { echo "FCLAZY FATAL firecracker download"; exit 1; }
echo "FCLAZY-ENV fc=$("$top/bin/firecracker" --version | head -1) sha256_tgz=$(cut -d' ' -f1 "$top/dl/firecracker-$FC_VERSION-x86_64.tgz.sha256.txt")"

CGO_ENABLED=0 go build -o "$top/bin/fcguest" ./scratch/fclazy/guest || { echo "FCLAZY FATAL guest build"; exit 1; }
CGO_ENABLED=0 go build -o "$top/bin/fclazy" ./scratch/fclazy || { echo "FCLAZY FATAL harness build"; exit 1; }

work=$top/w
if [ "$FS" = xfs ]; then
	command -v mkfs.xfs >/dev/null || apt-get install -qq -y xfsprogs >/dev/null 2>&1
	avail_gib=$(( $(df --output=avail "$top" | tail -1) / 1048576 ))
	truncate -s "$(( avail_gib - 12 ))G" "$top/xfs.img" && mkfs.xfs -q -m reflink=1 "$top/xfs.img" || { echo "FCLAZY FATAL mkfs.xfs"; exit 1; }
	# Direct I/O on the loop device: the image file gets no second page cache.
	loop=$(losetup --find --show --direct-io=on "$top/xfs.img") && mkdir -p "$top/xfs" && mount "$loop" "$top/xfs" || { echo "FCLAZY FATAL xfs mount"; exit 1; }
	work=$top/xfs/w
	echo "FCLAZY-ENV xfs_loop=$loop direct_io=$(losetup --list --noheadings --output DIO "$loop" | tr -d ' ') size_gib=$(( avail_gib - 12 ))"
fi
mkdir -p "$work"
echo "FCLAZY-ENV fs_work=$(df -T "$work" | tail -1 | awk '{print $2, "avail_gib=" int($5/1048576)}')"

# Root disk: the standard image flattened to one raw ext4 file, plus the
# guest checker as /fcguest.
t=$(date +%s)
docker pull -q "$IMAGE" >/dev/null || { echo "FCLAZY FATAL image pull"; exit 1; }
cid=$(docker create "$IMAGE") && mkdir -p "$top/root" && docker export "$cid" | tar -x -C "$top/root" || { echo "FCLAZY FATAL image export"; exit 1; }
docker rm "$cid" >/dev/null
cp "$top/bin/fcguest" "$top/root/fcguest"
mkdir -p "$top/root/data" "$top/root/proc" "$top/root/sys" "$top/root/dev" "$top/root/tmp"
root_mib=$(du -sm "$top/root" | cut -f1)
truncate -s "$(( root_mib * 13 / 10 + 512 ))M" "$work/rootfs.ext4"
mkfs.ext4 -q -F -d "$top/root" "$work/rootfs.ext4" || { echo "FCLAZY FATAL rootfs"; exit 1; }
rm -rf "$top/root"
truncate -s 2G "$work/data.raw" && mkfs.ext4 -q -F "$work/data.raw" || { echo "FCLAZY FATAL data disk"; exit 1; }
echo "FCLAZY-ENV image=$IMAGE digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$IMAGE") root_tree_mib=$root_mib rootfs_allocated_mib=$(( $(stat -c %b "$work/rootfs.ext4") / 2048 )) build_s=$(( $(date +%s) - t ))"

ip link del "$tap" 2>/dev/null
ip tuntap add dev "$tap" mode tap && ip addr add 172.31.77.1/30 dev "$tap" && ip link set "$tap" up || { echo "FCLAZY FATAL tap"; exit 1; }

sync
echo "FCLAZY-ENV disk_write: $(dd if=/dev/zero of="$work/w.bin" bs=1M count=1024 conv=fsync 2>&1 | tail -1)"
echo "FCLAZY-ENV disk_read_cold: $(python3 -c "import os,sys;fd=os.open(sys.argv[1],os.O_RDONLY);os.posix_fadvise(fd,0,0,os.POSIX_FADV_DONTNEED)" "$work/w.bin"; dd if="$work/w.bin" of=/dev/null bs=1M 2>&1 | tail -1)"
rm -f "$work/w.bin"

merge=inplace
[ "$FS" = xfs ] && merge=reflink
"$top/bin/fclazy" -fc "$top/bin/firecracker" -editor "$top/bin/snapshot-editor" -kernel "$kernel" \
	-rootfs "$work/rootfs.ext4" -data "$work/data.raw" -work "$work" -fs "$FS" -merge "$merge" -mem "$MEM" -tap "$tap" \
	-cycles "${FCLAZY_CYCLES:-12}" -mincore-cycles "${FCLAZY_MINCORE_CYCLES:-12}" -churn-min "${FCLAZY_CHURN_MIN:-10}" -n "${FCLAZY_N:-10}" \
	-direct-cycles "${FCLAZY_DIRECT_CYCLES:-4}" -mincore-thp="${FCLAZY_MINCORE_THP:-false}" -sections "${FCLAZY_SECTIONS:-chain,timing,idle,baseline,frag,mincore,direct,chcow}"
status=$?
echo "FCLAZY-ENV harness_exit=$status"
df -h "$work" | tail -1 | sed 's/^/FCLAZY-ENV df_end: /'
dmesg | grep -iE 'oom|out of memory|firecracker|segfault' | tail -20 | sed 's/^/FCLAZY-ENV host_dmesg: /'

ip link del "$tap" 2>/dev/null
[ "$FS" = xfs ] && { umount "$top/xfs"; losetup -d "$loop"; }
rm -rf "$top"
echo "FCLAZY DONE-SCRIPT"
