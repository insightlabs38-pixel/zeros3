/* Z3-26 independent VHDX reader oracle over libvhdi (system package, not a ZeroS3 dependency).
 * usage: vhdiread [-o OFF] [-l LEN] CHILD [PARENT ...]   -> raw logical bytes on stdout
 *        vhdiread -i CHILD                               -> info on stderr */
#include <libvhdi.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
int main(int argc, char **argv) {
  uint64_t off = 0, len = UINT64_MAX; int info = 0, i = 1;
  for (; i < argc && argv[i][0] == '-'; i++) {
    if (!strcmp(argv[i], "-o")) off = strtoull(argv[++i], 0, 10);
    else if (!strcmp(argv[i], "-l")) len = strtoull(argv[++i], 0, 10);
    else if (!strcmp(argv[i], "-i")) info = 1;
  }
  int n = argc - i; if (n < 1) return 2;
  libvhdi_file_t *f[8] = {0}; libvhdi_error_t *e = NULL;
  if (n > 8) return 2;
  for (int k = 0; k < n; k++) {
    if (libvhdi_file_initialize(&f[k], &e) != 1 || libvhdi_file_open(f[k], argv[i + k], LIBVHDI_OPEN_READ, &e) != 1) { libvhdi_error_fprint(e, stderr); return 1; }
  }
  for (int k = 0; k + 1 < n; k++) {
    if (libvhdi_file_set_parent_file(f[k], f[k + 1], &e) != 1) { libvhdi_error_fprint(e, stderr); return 1; }
  }
  size64_t sz = 0; uint32_t dt = 0, bs = 0, lss = 0;
  libvhdi_file_get_media_size(f[0], &sz, &e);
  libvhdi_file_get_disk_type(f[0], &dt, &e);
  libvhdi_file_get_bytes_per_sector(f[0], &lss, &e);
  if (info) { fprintf(stderr, "media_size=%llu disk_type=%u bytes_per_sector=%u\n", (unsigned long long)sz, dt, lss); return 0; }
  if (len == UINT64_MAX || off + len > sz) len = sz - off;
  static uint8_t buf[1 << 20];
  uint64_t done = 0;
  while (done < len) {
    size_t want = len - done > sizeof(buf) ? sizeof(buf) : (size_t)(len - done);
    ssize_t r = libvhdi_file_read_buffer_at_offset(f[0], buf, want, off + done, &e);
    if (r <= 0) { libvhdi_error_fprint(e, stderr); return 1; }
    fwrite(buf, 1, r, stdout); done += r;
  }
  return 0;
}
