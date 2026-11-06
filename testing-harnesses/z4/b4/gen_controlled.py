import random,sys
def gen(variant):
    r=random.Random(1234)
    out=['#include <string.h>','#include <stdio.h>','#include <stdlib.h>']
    nstruct=12
    structs=[]
    for i in range(nstruct):
        k=3+i%4
        fields="; ".join(f"{(['int','long','char *','double'][(i+j)%4] if j else 'int')} f{j}" for j in range(k))
        structs.append((f"S{i}",k,f"struct S{i} {{ {fields}; }};"))
    if variant=='type':
        out.append("struct Xnew { int a; double b; };\nstruct Xnew gx = {1, 2.5};\nint usex(int n) { return gx.a * n + (int)gx.b; }")
    out+= [s[2] for s in structs]
    globs=[f"int g{i} = {i*7};" for i in range(40)]
    if variant=='global': out.append("int gnew0 = 99; char gnew1[40];")
    out+=globs
    funcs=[]
    N=220
    for i in range(N):
        s=structs[r.randrange(nstruct)]
        body=[]
        body.append(f"int h{i}(void *vp, int n) {{")
        body.append(f"  struct {s[0]} *p = vp;")
        body.append("  int acc = 0;")
        body.append(f"  for (int i = 0; i < n; i++) {{ acc += p->f0 * (i + {r.randrange(1,50)}); if (acc > {r.randrange(100,9999)}) acc ^= g{r.randrange(40)}; }}")
        if r.random()<0.5: body.append(f'  if (acc == {r.randrange(1000)}) printf("fn{i}: %d\\n", acc);')
        if r.random()<0.3: body.append(f"  switch (n & 7) {{ case 1: acc += {r.randrange(9)}; break; case 3: acc -= {r.randrange(9)}; break; case 5: acc *= 3; break; default: break; }}")
        if i>0 and r.random()<0.6: body.append(f"  acc += (int)(long)h{r.randrange(i)}(p, n - 1 > 0 ? n - 1 : 0) * 0;" if False else f"  acc += h{r.randrange(i)}(p, n > 0 ? n - 1 : 0);")
        if r.random()<0.4: body.append(f"  acc += (int)strlen(\"literal-{i}-{r.randrange(100)}\");")
        body.append("  return acc; }")
        funcs.append((f"h{i}","\n".join(body)))
    if variant=='const':
        # change constants in a few functions
        for k in (10,60,120,180,200):
            n,t=funcs[k]; funcs[k]=(n,t.replace("acc > ","acc > 1"))
    if variant=='rename':
        import re
        funcs=[(re.sub(r"\bh7\b","hseven",n),re.sub(r"\bh7\b","hseven",t)) for n,t in funcs]
    if variant=='reorder':
        # keep declaration order valid: emit forward declarations then definitions reversed
        out+= [f"static int {n}(struct {s[0]} *p, int n);" for (n,_),s in zip(funcs,[None]*0)] if False else []
    if variant=='early':
        funcs.insert(0,("hnew","int hnew(void *vp, int n) { struct S0 *p = vp; return p->f0 + n * 31 + g3; }"))
    if variant=='late':
        funcs.append(("hlate","int hlate(void *vp, int n) { struct S1 *p = vp; return p->f0 - n * 13 + g9; }"))
    body="\n".join(t for _,t in funcs)
    out.append(body)
    # exported wrappers keep every static alive
    out.append("int run(void *base, int n) {")
    out.append("  int t = 0;") if False else None
    out.append("  int t = 0;")
    for i in range(0,N,3):
        out.append(f"  t += h{i}((void *)base, n);")
    if variant=='rename':
        pass
    if variant=='early': out.append("  t += hnew(base, n);")
    if variant=='type': out.append("  t += usex(n);")
    if variant=='late': out.append("  t += hlate(base, n);")
    out.append("  return t; }")
    return "\n".join(out)+"\n"
for v in ['base','early','late','type','global','const','rename','restored']:
    src=gen('base' if v=='restored' else v)
    open(f"{v}.c","w").write(src)
