struct S { int a; char *b; };
struct T { long x; struct S s; };
extern int g(int, ...);
extern int ext1(void *);
static int tab[4] = {1,2,3,4};
int counter = 5;
int h(struct S *s, int x) {
  int r = 0;
  for (int i = 0; i < x; i++) r += s->a * tab[i&3];
  if (r > 10) g(r, 3.5, "hi");
  switch (x) { case 1: r++; break; case 5: r+=7; break; default: break; }
  return r + (s->b ? 1 : 0) + counter;
}
int k(struct T *t, int y) { counter += y; return h(&t->s, y) + ext1(t); }
double dd(double a, float b) { return a * b + 1.5; }
