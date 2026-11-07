#!/usr/bin/env python3
"""Print the markdown tables used in the B5 report from the raw outputs. usage: report_tables.py OUT EV"""
import glob, json, os, re, sys
O, E = sys.argv[1], sys.argv[2]
def J(p):
    try: return json.load(open(p))
    except Exception: return None
def cdc(f): return [json.loads(l) for l in open(f)]
print('## T1 cumulative retained histories (charged bytes / production CDC)')
print('|family|states|raw MB|CDC MB|fine|Model8|B1 generic|B1 PXR ctl|B5 direct|first|prev|sel2|lin2|pool3|hybrid|best|best/strongest-existing|')
print('|'+'---|'*17)
for fam in ['pythia14m','pythia70m','smollm2','qwen05','gpt2']:
    p=f'{O}/{fam}/cdc.jsonl'
    if not os.path.exists(p): continue
    c=cdc(p); C=c[-1]['prod_cum']; raw=sum(x['raw'] for x in c)
    m8=(J(f'{E}/model8_{fam}.json') or {}).get('model8_total'); b1=(J(f'{E}/b1_{fam}.json') or {}).get('total',{})
    pol={os.path.basename(f)[:-5]:J(f)['total'] for f in glob.glob(f'{O}/{fam}/*.json') if '.verify' not in f}
    def r(x): return f'{x/C:.3f}' if x else '-'
    hyb=min([v for k,v in pol.items() if 'hyb' in k] or [0]); best=min(pol.values()) if pol else 0
    ex=min([x for x in (m8,b1.get('Generic'),b1.get('PXRControl')) if x] or [0])
    print(f"|{fam}|{len(c)}|{raw/1e6:.0f}|{C/1e6:.0f}|{r(c[-1]['fine_cum'])}|{r(m8)}|{r(b1.get('Generic'))}|{r(b1.get('PXRControl'))}|{r(pol.get('direct'))}|{r(pol.get('first'))}|{r(pol.get('prev'))}|{r(pol.get('sel2'))}|{r(pol.get('lin2'))}|{r(pol.get('pool3'))}|{r(hyb)}|{r(best)}|{best/ex:.3f}|" if ex else f"|{fam}|{len(c)}|{raw/1e6:.0f}|{C/1e6:.0f}|{r(c[-1]['fine_cum'])}|-|-|-|{r(pol.get('direct'))}|{r(pol.get('first'))}|{r(pol.get('prev'))}|{r(pol.get('sel2'))}|{r(pol.get('lin2'))}|{r(pol.get('pool3'))}|{r(hyb)}|{r(best)}|-|")
print('\n## T1b variants'); 
for fam in ['pythia70m','gpt2','smollm2ctl','qwenabl','pythia14m','smollm2']:
    c=cdc(f'{O}/{fam}/cdc.jsonl'); C=c[-1]['prod_cum']
    print(fam, 'CDC %.1f MB;'%(C/1e6), ', '.join(f"{os.path.basename(f)[:-5]} {J(f)['total']/C:.3f}" for f in sorted(glob.glob(f'{O}/{fam}/*.json')) if '.verify' not in f))
print('\n## T2 economics (best policy per family): standalone S, authoritative S+I, additive C+S+I; first-state ratio; closure of last state; max hop depth')
for fam in ['pythia14m','pythia70m','smollm2','qwen05','gpt2']:
    c=cdc(f'{O}/{fam}/cdc.jsonl'); C=c[-1]['prod_cum']
    pol={os.path.basename(f)[:-5]:J(f) for f in glob.glob(f'{O}/{fam}/*.json') if '.verify' not in f}
    if not pol: continue
    bn=min(pol,key=lambda k:pol[k]['total']); d=pol[bn]; st=d['states']
    I=44*len(st)+36*len(st)
    def closure(i,seen):
        if i in seen: return 0
        seen.add(i); return st[i]['charged']+sum(closure(j,seen) for j in (st[i]['bases'] or []))
    dep=[0]*len(st)
    for i,s in enumerate(st):
        for j in (s['bases'] or []): dep[j]+=1
    S=d['total']
    print(f"{fam} [{bn}] S={S/1e6:.1f} MB ({S/C:.3f}C) auth={(S+I)/1e6:.1f} additive={(C+S+I)/1e6:.1f} ({(C+S+I)/C:.3f}C) first={st[0]['charged']/c[0]['prod_inc']:.3f}xCDC last-closure={closure(len(st)-1,set())/1e6:.1f}MB ({closure(len(st)-1,set())/S:.2f} of S) max-dependents={max(dep)} depth={max(s['max_depth'] for s in st)} enc={sum(s['enc_secs'] for s in st):.0f}s")
print('\n## T4 quantized lane')
for n in ['smol_with_bf16','smol_gguf_only','qwen_with_bf16','qwen_gguf_only']:
    d=J(f'{O}/quant/{n}.json'); v=J(f'{O}/quant/{n}.verify.json')
    if not d: continue
    key={'smol':'smolq','qwen':'qwenq'}[n[:4]]+('only' if 'only' in n else '')
    c=cdc(f'{O}/quant/cdc_{key}.jsonl')[-1]
    m8=(J(f'{O}/quant/model8_{key}.json') or {}).get('model8_total')
    print(f"{n}: B5 {d['total']/1e6:.1f} MB vs CDC {c['prod_cum']/1e6:.1f} ({d['total']/c['prod_cum']:.3f}) fine {c['fine_cum']/1e6:.1f}"+(f" Model8 {m8/1e6:.1f}" if m8 else '')+f" exact {v['exact']}/{v['states']}")
    for s in d['states']: print(f"   {s['file'][:44]:44s} {s['raw']/1e6:7.1f} MB -> {s['charged']/1e6:8.3f} MB {{{', '.join(f'{k}:{x/1e3:.0f}K' for k,x in s['mode_bytes'].items())}}}")
