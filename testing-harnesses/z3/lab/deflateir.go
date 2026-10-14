package lab

import (
	"fmt"
	"io"
	"time"

	"zeros3-z3/deflate"
)

// "deflate-ir": the native exact RFC1951 parser/replayer (package deflate).
// Every raw DEFLATE stream (raw file, gzip payload, ZIP member) is parsed into
// the explicit in-memory IR; Rebuild replays it, and the lab compares the result
// with the original object by length and SHA-256. No physical encoding of the IR
// exists yet, so the row's cost is N/A (Plan.NoCost) rather than estimated.
// A stream the parser rejects is an error row, not silent fallback: valid input
// must parse.
func analyzeDeflateIR(in *Input) (*Plan, error) {
	regions, err := deflateRegions(in)
	if err != nil {
		return nil, err
	}
	opt := deflate.Options{MaxOutput: int(min(in.Opts.MaxSegBytes, 1<<30))}
	type parsed struct {
		region pfRegion
		s      *deflate.Stream
	}
	var ps []parsed
	var agg deflate.Stats
	var comp, plain int64
	t0 := time.Now()
	for _, rg := range regions {
		if !rg.Stream {
			continue
		}
		raw, err := in.Src.Span(rg.Off, rg.Len)
		if err != nil {
			return nil, err
		}
		s, err := deflate.Parse(raw, opt)
		if err != nil {
			return nil, fmt.Errorf("deflate-ir: stream at %d: %w", rg.Off, err)
		}
		ps = append(ps, parsed{rg, s})
		st := s.Stats()
		comp += int64(st.CompressedBytes)
		plain += int64(st.PlainBytes)
		agg.Blocks += st.Blocks
		agg.StoredBlocks += st.StoredBlocks
		agg.FixedBlocks += st.FixedBlocks
		agg.DynamicBlocks += st.DynamicBlocks
		agg.Tokens += st.Tokens
		agg.Literals += st.Literals
		agg.Matches += st.Matches
		agg.MatchBytes += st.MatchBytes
		agg.StoredBytes += st.StoredBytes
		agg.DynHeaderBits += st.DynHeaderBits
		agg.TokenBits += st.TokenBits
		agg.CLSyms += st.CLSyms
		agg.NaiveIRBytes += st.NaiveIRBytes
		agg.NonCanonicalLengths += st.NonCanonicalLengths
	}
	parseDur := time.Since(t0)
	detail := map[string]any{
		"streams": len(ps), "parsed_ok": len(ps), "compressed_bytes": comp, "plain_bytes": plain,
		"blocks": agg.Blocks, "stored_blocks": agg.StoredBlocks, "fixed_blocks": agg.FixedBlocks, "dynamic_blocks": agg.DynamicBlocks,
		"tokens": agg.Tokens, "literals": agg.Literals, "matches": agg.Matches, "match_bytes": agg.MatchBytes, "stored_bytes": agg.StoredBytes,
		"dyn_header_bytes": agg.DynHeaderBits / 8, "token_bytes": agg.TokenBits / 8, "naive_ir_bytes": agg.NaiveIRBytes,
		"noncanonical_lengths": agg.NonCanonicalLengths,
		"parse_ms":             ms(parseDur),
	}
	if parseDur > 0 {
		detail["parse_mb_per_s_plain"] = float64(plain) / (1 << 20) / parseDur.Seconds()
	}
	p := &Plan{NoCost: true, Detail: detail}
	p.Rebuild = func(w io.Writer, _ func(ID) ([]byte, error)) error {
		var replayDur time.Duration
		for _, rg := range regions {
			if rg.Stream {
				var s *deflate.Stream
				for _, q := range ps {
					if q.region == rg {
						s = q.s
					}
				}
				t := time.Now()
				err := s.Replay(w)
				replayDur += time.Since(t)
				if err != nil {
					return err
				}
			} else {
				b, err := in.Src.Span(rg.Off, rg.Len)
				if err != nil {
					return err
				}
				if _, err := w.Write(b); err != nil {
					return err
				}
			}
		}
		detail["replay_ms"] = ms(replayDur)
		if replayDur > 0 {
			detail["replay_mb_per_s_plain"] = float64(plain) / (1 << 20) / replayDur.Seconds()
		}
		return nil
	}
	return p, nil
}
