"""Mechanically adapt existing native producer to pinned Pebble v2.1.7. GO WORK."""
import pathlib, subprocess, sys
compiler, work = sys.argv[1], pathlib.Path(sys.argv[2]); work.mkdir(parents=True, exist_ok=True)
src = pathlib.Path(__file__).parents[1] / 'z3/oracle/sst/pebblegen'
for p in src.glob('*.go'):
    text = p.read_text().replace('github.com/cockroachdb/pebble', 'github.com/cockroachdb/pebble/v2')
    text = text.replace('pebble.SnappyCompression', 'sstable.SnappyCompression').replace('pebble.NoCompression', 'sstable.NoCompression').replace('pebble.ZstdCompression', 'sstable.ZstdCompression')
    if p.name == 'main.go':
        text = text.replace('import (', 'import (\n"context"\n"github.com/cockroachdb/pebble/v2/sstable"')
        text = text.replace(', Levels: make([]pebble.LevelOptions, 7)', '')
        text = text.replace('o.Levels[i].TargetFileSize', 'o.TargetFileSizes[i]')
        text = text.replace('o.Levels[i].Compression = codec', 'o.Levels[i].Compression = func() *sstable.CompressionProfile {return codec}')
        text = text.replace('db.Compact(', 'db.Compact(context.Background(),')
        text = text.replace('"v1.1.5"', '"v2.1.7"').replace('FormatNewest (table format Pebblev4)', 'FormatNewest (v2.1.7)')
    (work / p.name).write_text(text)
if not (work / 'go.mod').exists():
    subprocess.run([compiler, 'mod', 'init', 'zeros3-q8-pebble-new'], cwd=work, check=True)
subprocess.run([compiler, 'get', 'github.com/cockroachdb/pebble/v2@v2.1.7'], cwd=work, check=True)
subprocess.run([compiler, 'build', '-o', str(work / 'producer')], cwd=work, check=True)
