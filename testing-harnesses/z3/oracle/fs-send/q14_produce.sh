#!/bin/bash
# Run inside unshare -Urnm; only the dedicated file-backed vdev is overwritten.
# Dependencies: hash-pinned q14-tools and q13-tools; no host pool imports.
set -eu
export Q14_CACHE=${Q14_CACHE:-/workspace/work}
export LD_LIBRARY_PATH=${Q14_CACHE}/q13-tools/lib/x86_64-linux-gnu:${Q14_CACHE}/q13-tools/usr/lib/x86_64-linux-gnu
mount --make-rprivate /
mount --bind ${Q14_CACHE}/q14-probe/run /run
${Q14_CACHE}/q14-tools/sbin/zfs-fuse --no-daemon --no-kstat-mount --max-arc-size 64 --zfs-prefetch-disable --pidfile /run/q14.pid > ${Q14_CACHE}/q14-probe/daemon.log 2>&1 &
daemon=$!
trap 'kill "$daemon" 2>/dev/null || true; wait "$daemon" 2>/dev/null || true' EXIT
for attempt in $(seq 1 30); do
 if ${Q14_CACHE}/q14-tools/sbin/zpool list; then break; fi
 kill -0 "$daemon"
 sleep 1
done
truncate -s 256M ${Q14_CACHE}/q14-probe/vdev
${Q14_CACHE}/q14-tools/sbin/zpool create -f -m ${Q14_CACHE}/q14-probe/mnt zeros3q14 ${Q14_CACHE}/q14-probe/vdev

export PATH=${Q14_CACHE}/q14-tools/sbin:$PATH
mkdir -p ${Q14_CACHE}/q14-corpus/legacy
zfs create zeros3q14/src
python3 - <<'INNER'
import random,pathlib,os
p=pathlib.Path(os.environ['Q14_CACHE'])/'q14-probe/mnt/src'
r=random.Random(1042)
(p/'data').write_bytes(r.randbytes(2<<20))
(p/'repeat').write_bytes((b'ZeroS3 native send workload '+bytes(range(128)))*8192)
INNER
for n in 0 1 2 3; do
 if [ "$n" = 1 ]; then
  python3 -c "p='${Q14_CACHE}/q14-probe/mnt/src/data'; f=open(p,'r+b'); f.seek(65536); f.write(b'changed native snapshot bytes'); f.close()"
 fi
 if [ "$n" = 2 ]; then
  head -c 65536 ${Q14_CACHE}/q14-probe/mnt/src/repeat >> ${Q14_CACHE}/q14-probe/mnt/src/data
 fi
 if [ "$n" = 3 ]; then
  mv ${Q14_CACHE}/q14-probe/mnt/src/repeat ${Q14_CACHE}/q14-probe/mnt/src/renamed
 fi
 zfs snapshot zeros3q14/src@s$n
 zfs send zeros3q14/src@s$n > ${Q14_CACHE}/q14-corpus/legacy/full$n.zsend
 if [ "$n" != 0 ]; then
  prev=$((n-1))
  zfs send -i zeros3q14/src@s$prev zeros3q14/src@s$n > ${Q14_CACHE}/q14-corpus/legacy/incr$n.zsend
 fi
done
zfs send zeros3q14/src@s3 > ${Q14_CACHE}/q14-corpus/legacy/full3-repeat.zsend
zfs receive -u zeros3q14/restored < ${Q14_CACHE}/q14-corpus/legacy/full0.zsend
for n in 1 2 3; do zfs receive -u zeros3q14/restored < ${Q14_CACHE}/q14-corpus/legacy/incr$n.zsend; done
zfs mount zeros3q14/restored
diff -r ${Q14_CACHE}/q14-probe/mnt/src ${Q14_CACHE}/q14-probe/mnt/restored
zfs list -t snapshot
for profile in off lzjb; do
 zfs create -o compression=$profile zeros3q14/independent-$profile
 cp -a ${Q14_CACHE}/q14-probe/mnt/src/. ${Q14_CACHE}/q14-probe/mnt/independent-$profile/
 zfs snapshot zeros3q14/independent-$profile@s3
 zfs send zeros3q14/independent-$profile@s3 > ${Q14_CACHE}/q14-corpus/legacy/independent-$profile.zsend
done
zpool destroy zeros3q14
