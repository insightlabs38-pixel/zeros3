#!/bin/sh
# Z3-26 optional disk-image oracles (outside the Go module; never a ZeroS3 dependency).
#   vhdxgen : pinned vhdx-rs 0.2.0 producer of dynamic + differencing VHDX (needs cargo)
#   vhdiread: libvhdi-based independent reader / chain reader (needs gcc + libvhdi-dev)
#   qemu-img: QCOW2 producer (apt: qemu-utils)
# Usage: oracle/disk_setup.sh   (run from testing-harnesses/z3)
set -e
here=$(cd "$(dirname "$0")" && pwd)
(cd "$here/vhdxgen" && cargo build --release --locked)
gcc -O2 -o "$here/vhdiread/vhdiread" "$here/vhdiread/vhdiread.c" -lvhdi
echo "built: $here/vhdxgen/target/release/vhdxgen $here/vhdiread/vhdiread"
