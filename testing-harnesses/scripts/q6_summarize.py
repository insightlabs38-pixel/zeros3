"""Verify and summarize captured physical policy replay: script WORK OUTPUT_JSON."""
from pathlib import Path
import gzip,json,sys,hashlib
root=Path(sys.argv[1]);frontier=json.loads(gzip.decompress((root/'frontier.json.gz').read_bytes()));rows=[json.loads(x) for x in (root/'final-policies.jsonl').read_text().splitlines()]
if len(rows)!=len(frontier['Cases'])*7 or any(not x['OracleExact'] or x['Regret']<0 for x in rows):raise RuntimeError('incomplete/nonexact replay')
public=json.loads((root/'public-provenance.json').read_text());producer=json.loads((root/'producer-provenance.json').read_text())
pins={x['sha256'] for x in public if 'sha256' in x}|{x['sha256'] for x in producer}
git=json.loads((Path(__file__).resolve().parents[1]/'testdata/i1c-git/real-reexports.json').read_text());pins|={x['sha256'] for x in git['files']}
for case in frontier['Cases']:
 if case['Class'] in ['merkle','parquet']:continue
 if any(s['InputSHA'] not in pins for s in case['Stages']):raise RuntimeError('unpinned frontier input '+case['Name'])
summary={'frontier_sha256':hashlib.sha256((root/'frontier.json.gz').read_bytes()).hexdigest(),'cases':len(frontier['Cases']),'objects':len(frontier['Objects']),'object_overhead':frontier['ObjectOverhead'],'groups':{},'rows':rows}
for group in ['primary','one-off','no-hint']:
 out={}
 for policy in ['greedy','premium-cap','late-bootstrap','one-step','expected-value','beam','oracle']:
  r=[x for x in rows if x['Policy']==policy and (not x['Case'].startswith(('one-off/','no-hint/')) if group=='primary' else x['Case'].startswith(group+'/'))]
  total=sum(x['Total'] for x in r);oracle=sum(x['Oracle'] for x in r)
  out[policy]={'total':total,'oracle':oracle,'regret_fraction':total/oracle-1,'bad_admissions':sum(x['BadAdmissions'] for x in r),'missed_lineages':sum(x['MissedLineages'] for x in r),'max_first_premium':max(x['FirstPremium'] for x in r),'analysis_ns':sum(x['AnalysisNS'] for x in r),'bootstrap_redo_analysis_ns':sum(x['BootstrapRedoAnalysisNS'] for x in r),'logical_read_bytes':sum(x['LogicalReadBytes'] for x in r),'extra_read_bytes':sum(x['ExtraReadBytes'] for x in r),'bootstrap_redo_read_upper':sum(x['BootstrapRedoReadUpper'] for x in r),'peak_input_buffer':max(x['BufferedBytes'] for x in r),'sample_payload_bytes':max(x['SampleStateBytes'] for x in r),'membership_bytes':max(x['MembershipBytes'] for x in r),'prefetch_bytes':sum(x['EarlyReadBytes'] for x in r),'feature_digest_bytes':sum(x['SketchBytes'] for x in r)}
 summary['groups'][group]=out
summary['sensitivity']={}
for overhead in [0,256]:
 r=[json.loads(x) for x in (root/f'overhead{overhead}.jsonl').read_text().splitlines()];out={}
 for policy in ['greedy','premium-cap','late-bootstrap','one-step','expected-value','beam','oracle']:
  a=[x for x in r if x['Policy']==policy and not x['Case'].startswith(('one-off/','no-hint/'))];out[policy]={'cost':sum(x['Total'] for x in a),'oracle':sum(x['Oracle'] for x in a),'bad':sum(x['BadAdmissions'] for x in a),'missed':sum(x['MissedLineages'] for x in a)}
 summary['sensitivity'][str(overhead)]=out
Path(sys.argv[2]).write_text(json.dumps(summary,indent=2)+'\n')
