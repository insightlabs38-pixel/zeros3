// z4brotliresidual measures byte-exact residuals against real Parquet page bodies.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"zeros3-z3/brotliz3"
	_ "zeros3-z3/brotliz3/brdict"
	"zeros3-z3/dcore"
	"zeros3-z3/parquetz3"
)

const reference = `
import brotli, sys
data = sys.stdin.buffer.read()
if sys.argv[1] == "decode":
    result = brotli.decompress(data)
else:
    quality = int(sys.argv[1])
    if sys.argv[2] == "one-shot":
        result = brotli.compress(data, quality=quality, lgwin=22)
    else:
        c = brotli.Compressor(quality=quality, lgwin=22)
        result = b"".join(c.process(data[i:i+65536]) + c.flush()
                          for i in range(0, len(data), 65536)) + c.finish()
sys.stdout.buffer.write(result)
`

type measurement struct {
	Offset          int64   `json:"page_offset"`
	Column          string  `json:"column"`
	RawBytes        int     `json:"body_bytes"`
	PredictorBytes  int     `json:"predictor_bytes"`
	Quality         int     `json:"quality,omitempty"`
	Producer        string  `json:"producer"`
	Mode            string  `json:"mode"`
	OpaqueBytes     int     `json:"opaque_bytes"`
	ResidualBytes   int     `json:"residual_bytes"`
	TargetSHA256    string  `json:"target_sha256"`
	PredictorMS     float64 `json:"predictor_ms"`
	ResidualBuildMS float64 `json:"residual_build_ms"`
	ResidualReadMS  float64 `json:"residual_read_ms"`
	Exact           bool    `json:"exact"`
}

type sample struct {
	col  string
	page parquetz3.Page
}

func ref(python string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.Command(python, append([]string{"-c", reference}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func measure(input sample, raw, target, predictor []byte, predictorMS float64, index *dcore.WIndex,
	params dcore.WParams, quality int, producer, mode string) (measurement, error) {
	m := measurement{
		Offset: input.page.Off, Column: input.col, RawBytes: len(raw), PredictorBytes: len(predictor),
		Quality: quality, Producer: producer, Mode: mode, OpaqueBytes: len(target),
		TargetSHA256: sha(target), PredictorMS: predictorMS,
	}
	var patch bytes.Buffer
	base := bytes.NewReader(predictor)
	start := time.Now()
	_, err := dcore.WEncode(&patch, bytes.NewReader(target), int64(len(target)), base, int64(len(predictor)), index, params)
	if err != nil {
		return m, err
	}
	m.ResidualBuildMS = millis(time.Since(start))
	m.ResidualBytes = patch.Len()
	start = time.Now()
	record, err := dcore.OpenWRecord(bytes.NewReader(patch.Bytes()), int64(patch.Len()))
	if err != nil {
		return m, err
	}
	var replay bytes.Buffer
	if _, err := record.Decode(&replay, base, params); err != nil {
		return m, err
	}
	m.ResidualReadMS = millis(time.Since(start))
	m.Exact = bytes.Equal(replay.Bytes(), target)
	if !m.Exact {
		return m, fmt.Errorf("byte mismatch at page %d", input.page.Off)
	}
	return m, nil
}

func run(path string, interpreters []string, limit int) error {
	if limit <= 0 || len(interpreters) == 0 {
		return errors.New("provide a positive sample count and at least one Python Brotli interpreter")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	meta, err := parquetz3.Open(f, fi.Size(), parquetz3.Limits{})
	if err != nil {
		return err
	}
	var candidates []sample
	for _, rg := range meta.RowGroups {
		for _, c := range rg.Chunks {
			if c.Codec != parquetz3.CodecBrotli {
				continue
			}
			err := meta.Pages(f, &c, func(p *parquetz3.Page, _ []byte) error {
				if p.Type != parquetz3.PageDataV2 && p.Uncomp >= 32<<10 && p.Uncomp <= 1<<20 {
					candidates = append(candidates, sample{meta.Columns[c.Col].Name, *p})
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	if len(candidates) == 0 {
		return errors.New("no supported Brotli pages in the requested size band")
	}
	limit = min(limit, len(candidates))
	versions := make([]string, len(interpreters))
	for i, python := range interpreters {
		version, err := exec.Command(python, "-c", "import brotli; print(brotli.__version__)").Output()
		if err != nil {
			return err
		}
		versions[i] = "Python Brotli " + strings.TrimSpace(string(version))
	}
	var rows []measurement
	for slot := range limit {
		p := candidates[(2*slot+1)*len(candidates)/(2*limit)]
		original, err := parquetz3.ReadPayload(f, &p.page)
		if err != nil {
			return err
		}
		raw, err := ref(interpreters[0], original, "decode")
		if err != nil {
			return err
		}
		if len(raw) != int(p.page.Uncomp) {
			return fmt.Errorf("original page %d has unexpected decompressed length", p.page.Off)
		}
		start := time.Now()
		predictor, err := brotliz3.Encode(raw, 9, 22)
		if err != nil {
			return err
		}
		predictorMS := millis(time.Since(start))
		decompressed, err := ref(interpreters[0], predictor, "decode")
		if err != nil {
			return err
		}
		if !bytes.Equal(decompressed, raw) {
			return fmt.Errorf("predictor body mismatch at page %d", p.page.Off)
		}
		params := dcore.DefaultWParams()
		index, err := dcore.BuildWIndex(bytes.NewReader(predictor), int64(len(predictor)), params.Anchor, params.Workers)
		if err != nil {
			return err
		}
		m, err := measure(p, raw, original, predictor, predictorMS, index, params, 0, meta.CreatedBy, "original")
		if err != nil {
			return err
		}
		rows = append(rows, m)
		for i, python := range interpreters {
			for _, quality := range []int{9, 10, 11} {
				for _, mode := range []string{"one-shot", "flush-64KiB"} {
					target, err := ref(python, raw, fmt.Sprint(quality), mode)
					if err != nil {
						return err
					}
					body, err := ref(interpreters[(i+1)%len(interpreters)], target, "decode")
					if err != nil {
						return err
					}
					if !bytes.Equal(body, raw) {
						return fmt.Errorf("reference body mismatch for %s q%d %s", versions[i], quality, mode)
					}
					m, err := measure(p, raw, target, predictor, predictorMS, index, params, quality, versions[i], mode)
					if err != nil {
						return err
					}
					rows = append(rows, m)
				}
			}
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(struct {
		ParquetSHA256 string        `json:"parquet_sha256"`
		EligiblePages int           `json:"eligible_pages"`
		SampledPages  int           `json:"sampled_pages"`
		Predictor     string        `json:"predictor"`
		Measurements  []measurement `json:"measurements"`
	}{hex.EncodeToString(sum.Sum(nil)), len(candidates), limit, "brotliz3 q9 lgwin22 plus dcore window delta", rows})
}

func main() {
	source := flag.String("parquet", "", "input Parquet file containing Brotli-compressed pages")
	python := flag.String("python", "python3", "comma-separated Python interpreters with independent Brotli versions")
	samples := flag.Int("samples", 8, "number of stratified pages to measure")
	flag.Parse()
	if err := run(*source, strings.Split(*python, ","), *samples); err != nil {
		log.Fatal(err)
	}
}
