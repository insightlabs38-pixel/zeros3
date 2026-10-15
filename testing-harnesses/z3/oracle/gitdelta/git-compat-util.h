/* Minimal shim so Git's diff-delta.c (fetched, never committed) compiles
 * standalone. Our code only; no Git source is vendored. */
#ifndef Z3_GIT_COMPAT_UTIL_H
#define Z3_GIT_COMPAT_UTIL_H
#include <assert.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#define FLEX_ARRAY /* empty */
static inline size_t st_left_shift(size_t a, unsigned s) { return a << s; }
static inline unsigned long cast_size_t_to_ulong(size_t v) { return (unsigned long)v; }
#endif
