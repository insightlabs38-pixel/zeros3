// Producer oracle: drives llama.cpp's own ggml-quants.c (not part of the Z3 module, not a runtime
// dependency). usage: qoracle TYPE N_PER_ROW [IMATRIX_F32] < f32 > blocks
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>
#include "ggml.h"
#include "ggml-quants.h"
#include "ggml-impl.h"

float ggml_table_f32_f16[1 << 16];
size_t ggml_type_size(enum ggml_type t) { (void)t; return 1; }
const char *ggml_type_name(enum ggml_type t) { (void)t; return "?"; }
static float f16_to_f32(uint16_t h) { // IEEE half -> float, exact
    uint32_t s = (uint32_t)(h >> 15) << 31, e = (h >> 10) & 31, m = h & 1023, u;
    if (e == 0) { if (m == 0) u = s; else { e = 1; while (!(m & 1024)) { m <<= 1; e--; } u = s | ((e + 112) << 23) | ((m & 1023) << 13); } }
    else if (e == 31) u = s | 0x7f800000u | (m << 13);
    else u = s | ((e + 112) << 23) | (m << 13);
    float f; memcpy(&f, &u, 4); return f;
}
size_t ggml_row_size(enum ggml_type type, int64_t ne) {
    switch (type) { // {elements, bytes} per block
    case GGML_TYPE_Q4_0: return ne / 32 * 18; case GGML_TYPE_Q4_1: return ne / 32 * 20;
    case GGML_TYPE_Q5_0: return ne / 32 * 22; case GGML_TYPE_Q5_1: return ne / 32 * 24;
    case GGML_TYPE_Q8_0: return ne / 32 * 34; case GGML_TYPE_Q2_K: return ne / 256 * 84;
    case GGML_TYPE_Q3_K: return ne / 256 * 110; case GGML_TYPE_Q4_K: return ne / 256 * 144;
    case GGML_TYPE_Q5_K: return ne / 256 * 176; case GGML_TYPE_Q6_K: return ne / 256 * 210;
    case GGML_TYPE_IQ4_NL: return ne / 32 * 18; case GGML_TYPE_IQ4_XS: return ne / 256 * 136;
    case GGML_TYPE_IQ3_S: return ne / 256 * 110; case GGML_TYPE_IQ3_XXS: return ne / 256 * 98;
    default: fprintf(stderr, "row_size type %d\n", type); exit(5);
    }
}

void ggml_critical_section_start(void) {}
void ggml_critical_section_end(void) {}
void ggml_abort(const char *file, int line, const char *fmt, ...) { fprintf(stderr, "abort %s:%d %s\n", file, line, fmt); exit(3); }

static void *slurp(FILE *f, size_t *n) {
    size_t cap = 1 << 20, len = 0; char *b = malloc(cap);
    for (;;) { size_t r = fread(b + len, 1, cap - len, f); len += r; if (r == 0) break; if (len == cap) b = realloc(b, cap *= 2); }
    *n = len; return b;
}

int main(int argc, char **argv) {
    if (argc < 3) return 2;
    for (int i = 0; i < (1 << 16); i++) ggml_table_f32_f16[i] = f16_to_f32((uint16_t)i);
    const char *t = argv[1]; int64_t npr = atoll(argv[2]);
    size_t nb, nq = 0; float *x = slurp(stdin, &nb); float *qw = NULL;
    if (argc > 3) { FILE *f = fopen(argv[3], "rb"); if (!f) return 2; qw = slurp(f, &nq); fclose(f); }
    int64_t n = nb / 4, nrow = n / npr;
    void *dst = malloc(nb + 4096); size_t out = 0;
#define Q(name, fn) if (!strcmp(t, name)) out = fn(x, dst, nrow, npr, qw);
    Q("Q4_0", quantize_q4_0) Q("Q4_1", quantize_q4_1) Q("Q5_0", quantize_q5_0) Q("Q5_1", quantize_q5_1) Q("Q8_0", quantize_q8_0)
    Q("Q2_K", quantize_q2_K) Q("Q3_K", quantize_q3_K) Q("Q4_K", quantize_q4_K) Q("Q5_K", quantize_q5_K) Q("Q6_K", quantize_q6_K)
    Q("IQ4_NL", quantize_iq4_nl) Q("IQ4_XS", quantize_iq4_xs)
    if (!strcmp(t, "IQ3_S")) { iq3xs_init_impl(512); out = quantize_iq3_s(x, dst, nrow, npr, qw); }
    if (!strcmp(t, "IQ3_XXS")) { iq3xs_init_impl(256); out = quantize_iq3_xxs(x, dst, nrow, npr, qw); }
    if (!out) return 4;
    fwrite(dst, 1, out, stdout);
    return 0;
}
