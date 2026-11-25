#!/usr/bin/env bash
# Install isolated competitors under a caller-owned scratch directory.
set -euo pipefail
work=$(realpath -m "${1:?usage: install.sh WORK SYSTEM ...}")
shift
mkdir -p "$work/bin" "$work/install"
cd "$work/install"
fetch() { curl --fail --silent --show-error --location --max-time 180 "$1" -o "$2"; }
for system in "$@"; do
  case "$system" in
    restic)
      fetch https://github.com/restic/restic/releases/download/v0.20.0/restic_0.20.0_linux_amd64.bz2 restic.bz2
      bzip2 -dc restic.bz2 > "$work/bin/restic"
      rm restic.bz2
      ;;
    borg)
      fetch https://github.com/borgbackup/borg/releases/download/1.4.5/borg-linux-glibc235-x86_64-gh "$work/bin/borg"
      ;;
    kopia)
      fetch https://github.com/kopia/kopia/releases/download/v0.23.1/kopia_0.23.1_linux_amd64.deb kopia.deb
      dpkg-deb -x kopia.deb kopia-pkg
      cp kopia-pkg/usr/bin/kopia "$work/bin/kopia"
      rm -r kopia-pkg kopia.deb
      ;;
    weed)
      fetch https://github.com/seaweedfs/seaweedfs/releases/download/4.55/linux_amd64.tar.gz weed.tgz
      tar -xzf weed.tgz -C "$work/bin" weed
      rm weed.tgz
      ;;
    rustfs)
      fetch https://github.com/rustfs/rustfs/releases/download/1.0.6/rustfs-linux-x86_64-gnu-v1.0.6.zip rustfs.zip
      unzip -oj rustfs.zip rustfs -d "$work/bin"
      rm rustfs.zip
      ;;
    garage)
      fetch https://garagehq.deuxfleurs.fr/_releases/v2.2.0/x86_64-unknown-linux-musl/garage "$work/bin/garage"
      ;;
    minio)
      GOCACHE="$work/minio-go-build" GOMODCACHE="$work/minio-go-mod" GOBIN="$work/bin" \
        "${C1_GO:-go}" install -p 2 github.com/minio/minio@v0.0.0-20250907161309-07c3a429bfed
      ;;
    openxet)
      CARGO_TARGET_DIR="$work/openxet-build" "${C1_CARGO:-cargo}" install --jobs 2 \
        --root "$work" --locked --git https://github.com/stubbi/openxet \
        --rev 11c7fea1e4d651365755432b67964d7ed4b53fea openxet
      ;;
    *) printf 'Unknown system %s\n' "$system" >&2; exit 1 ;;
  esac
  chmod +x "$work/bin/$system"
done
