// z4erofslz4 checks exact block replay on actual EROFS images diagnosed with dump.erofs.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"zeros3-z3/frontier"
	"zeros3-z3/lz4z3"
)

var extentRow = regexp.MustCompile(`^\s*\d+:\s*(\d+)\.\.\s*(\d+)\s*\|\s*(\d+)\s*:\s*(\d+)\.\.\s*(\d+)\s*\|\s*(\d+)`)

type extent struct {
	Logical  int64 `json:"logical"`
	Plain    int   `json:"plain_bytes"`
	Physical int64 `json:"physical"`
	Stored   int   `json:"cluster_bytes"`
}

func parseExtents(path string) ([]extent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows := bufio.NewScanner(f)
	var extents []extent
	for rows.Scan() {
		fields := extentRow.FindStringSubmatch(rows.Text())
		if len(fields) == 0 {
			continue
		}
		var values [6]int64
		for i := range values {
			x, err := strconv.ParseInt(fields[i+1], 10, 64)
			if err != nil {
				return nil, err
			}
			values[i] = x
		}
		b, e, logical, p, q, physical := values[0], values[1], values[2], values[3], values[4], values[5]
		if e < b || q < p || e-b != logical || q-p != physical {
			return nil, errors.New("inconsistent dump.erofs extent")
		}
		if logical > physical && logical <= 1<<20 && physical == 4096 {
			extents = append(extents, extent{b, int(logical), p, int(physical)})
		}
	}
	return extents, rows.Err()
}

func compactExtents(in *os.File, member string) ([]extent, error) {
	info, err := in.Stat()
	if err != nil {
		return nil, err
	}
	layout, err := frontier.ScanErofs(in, info.Size(), nil, 4096)
	if err != nil {
		return nil, err
	}
	member = strings.TrimPrefix(path.Clean(member), "/")
	f, ok := layout.Files[member]
	if !ok {
		return nil, fmt.Errorf("no EROFS file %q", member)
	}
	parsed, err := frontier.ErofsCompactLZ4Extents(in, info.Size(), layout, f)
	if err != nil {
		return nil, err
	}
	var spans []extent
	for _, p := range parsed {
		if p.LLen > p.PLen && p.LLen <= 1<<20 && p.PLen == 4096 {
			spans = append(spans, extent{p.LOff, int(p.LLen), p.POff, int(p.PLen)})
		}
	}
	return spans, nil
}

func loadExtents(in *os.File, oracle, member string) ([]extent, error) {
	if oracle == "" {
		return compactExtents(in, member)
	}
	return parseExtents(oracle)
}

type sample struct {
	Extent        extent `json:"extent"`
	SourceBytes   int    `json:"source_bytes"`
	ZeroPadding   int    `json:"zero_padding"`
	RecipeBytes   int    `json:"recipe_bytes"`
	ExplicitBytes int    `json:"explicit_recipe_bytes"`
	RecipeKind    int    `json:"recipe_kind"`
	BuildUS       int64  `json:"recipe_build_us"`
	ReplayUS      int64  `json:"replay_us"`
	ClusterSHA256 string `json:"cluster_sha256"`
	Exact         bool   `json:"exact"`
	Unsupported   string `json:"unsupported,omitempty"`
}

func inspect(image, source io.ReaderAt, x extent) (sample, error) {
	m := sample{Extent: x}
	raw := make([]byte, x.Stored)
	plain := make([]byte, x.Plain)
	if _, err := image.ReadAt(raw, x.Physical); err != nil {
		return m, err
	}
	if _, err := source.ReadAt(plain, x.Logical); err != nil {
		return m, err
	}
	m.ClusterSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	compressed := bytes.TrimLeft(raw, "\x00")
	m.ZeroPadding, m.SourceBytes = len(raw)-len(compressed), len(compressed)
	lim := lz4z3.Limits{}
	decoded, seqs, err := lz4z3.Decode(compressed, len(plain), lim)
	if err != nil || !bytes.Equal(decoded, plain) {
		m.Unsupported = fmt.Sprintf("raw block differed from independently extracted interval: %v", err)
		return m, nil
	}
	start := time.Now()
	recipe := lz4z3.Analyze(plain, seqs, true)
	m.BuildUS = time.Since(start).Microseconds()
	encoded := recipe.Marshal()
	m.RecipeBytes = len(encoded)
	m.ExplicitBytes = len((lz4z3.Recipe{Kind: lz4z3.KindExplicit, Seqs: seqs}).Marshal())
	m.RecipeKind = recipe.Kind
	start = time.Now()
	restored, n, err := lz4z3.Unmarshal(encoded, lim)
	if err != nil || n != len(encoded) {
		return m, fmt.Errorf("invalid encoded recipe: %w", err)
	}
	output, err := restored.Replay(nil, plain, lim)
	m.ReplayUS = time.Since(start).Microseconds()
	m.Exact = err == nil && bytes.Equal(output, compressed)
	if !m.Exact {
		return m, fmt.Errorf("stored block at %d was not replayed exactly: %v", x.Physical, err)
	}
	return m, nil
}

func run(imagePath, extentsPath, member, sourcePath string, count int) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	image, err := os.Open(imagePath)
	if err != nil {
		return err
	}
	defer image.Close()
	extents, err := loadExtents(image, extentsPath, member)
	if err != nil {
		return err
	}
	if len(extents) == 0 || count <= 0 {
		return errors.New("no compressed clusters or invalid sample count")
	}
	type result struct {
		ImageSHA256  string   `json:"image_sha256"`
		SourceSHA256 string   `json:"source_sha256"`
		Eligible     int      `json:"eligible_clusters"`
		Samples      []sample `json:"samples"`
	}
	report := result{Eligible: len(extents)}
	for _, p := range []struct {
		file *os.File
		dest *string
	}{{source, &report.SourceSHA256}, {image, &report.ImageSHA256}} {
		h := sha256.New()
		if _, err := io.Copy(h, p.file); err != nil {
			return err
		}
		*p.dest = fmt.Sprintf("%x", h.Sum(nil))
	}
	count = min(count, len(extents))
	for i := range count {
		x := extents[(2*i+1)*len(extents)/(2*count)]
		row, err := inspect(image, source, x)
		if err != nil {
			return err
		}
		report.Samples = append(report.Samples, row)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func runRecipe(image, extents, member, source, build, replay string) error {
	basis, err := os.Open(source)
	if err != nil {
		return err
	}
	defer basis.Close()
	if replay != "" {
		record, err := os.Open(replay)
		if err != nil {
			return err
		}
		defer record.Close()
		return replayRecipe(record, basis, os.Stdout)
	}
	original, err := os.Open(image)
	if err != nil {
		return err
	}
	defer original.Close()
	spans, err := loadExtents(original, extents, member)
	if err != nil {
		return err
	}
	record, err := os.OpenFile(build, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	report, err := makeRecipe(original, basis, spans, record)
	if cerr := record.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func main() {
	image := flag.String("image", "", "EROFS image")
	extents := flag.String("extents", "", "optional dump.erofs -e for the matched file")
	member := flag.String("member", "/source.tar", "EROFS file whose compact index is inspected")
	source := flag.String("source", "", "original file passed to mkfs.erofs")
	count := flag.Int("samples", 64, "number of stratified compressed clusters")
	build := flag.String("recipe-out", "", "write a full offline exact-cluster recipe")
	replay := flag.String("replay", "", "replay a full recipe to stdout")
	index := flag.String("index-out", "", "measure exact indexed ranges using the recipe supplied to -replay")
	flag.Parse()
	if *index != "" {
		if err := runIndexedRange(*image, *source, *replay, *index); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *build != "" || *replay != "" {
		if err := runRecipe(*image, *extents, *member, *source, *build, *replay); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(*image, *extents, *member, *source, *count); err != nil {
		log.Fatal(err)
	}
}
