/* z3 research oracle: Git's create_delta() behind a binary stdin/stdout protocol.
 * request : u32le base_len, u32le target_len, base bytes, target bytes
 * response: u32le delta_len (0xffffffff = no delta produced), delta bytes
 * EOF on stdin ends the session. */
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include "git-compat-util.h"
#include "delta.h"

static int rd(void *p, size_t n) { return fread(p, 1, n, stdin) == n; }
static void put32(uint32_t v) {
	unsigned char b[4] = { v, v >> 8, v >> 16, v >> 24 };
	fwrite(b, 1, 4, stdout);
}
int main(void) {
	unsigned char h[8];
	while (fread(h, 1, 8, stdin) == 8) {
		uint32_t bl = h[0] | h[1] << 8 | h[2] << 16 | (uint32_t)h[3] << 24;
		uint32_t tl = h[4] | h[5] << 8 | h[6] << 16 | (uint32_t)h[7] << 24;
		unsigned char *base = malloc(bl ? bl : 1), *tgt = malloc(tl ? tl : 1);
		if (!base || !tgt || !rd(base, bl) || !rd(tgt, tl)) return 2;
		unsigned long dsz = 0;
		struct delta_index *ix = create_delta_index(base, bl);
		void *d = ix ? create_delta(ix, tgt, tl, &dsz, 0) : NULL;
		if (!d) put32(0xffffffffu);
		else { put32((uint32_t)dsz); fwrite(d, 1, dsz, stdout); }
		fflush(stdout);
		free(d);
		if (ix) free_delta_index(ix);
		free(base); free(tgt);
	}
	return 0;
}
