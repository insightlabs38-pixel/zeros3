package frontier

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// Span is a byte range of a blob.
type Span struct{ Off, Len int64 }

// GzipMembers finds the exact byte span of every gzip member in b by decoding
// each one (output discarded) and reading how many input bytes it consumed.
// It fails closed: trailing garbage or a bad member is an error, so the spans
// always tile b exactly (concatenation of members == b).
func GzipMembers(b []byte, maxMembers int) ([]Span, error) {
	br := bytes.NewReader(b)
	var zr *gzip.Reader
	var spans []Span
	for br.Len() > 0 {
		if len(spans) >= maxMembers {
			return spans, fmt.Errorf("gzip: more than %d members", maxMembers)
		}
		start := int64(len(b) - br.Len())
		if zr == nil {
			var err error
			if zr, err = gzip.NewReader(br); err != nil {
				return spans, fmt.Errorf("gzip member at %d: %w", start, err)
			}
		} else if err := zr.Reset(br); err != nil {
			return spans, fmt.Errorf("gzip member at %d: %w", start, err)
		}
		zr.Multistream(false)
		if _, err := io.Copy(io.Discard, zr); err != nil {
			return spans, fmt.Errorf("gzip member at %d: %w", start, err)
		}
		spans = append(spans, Span{start, int64(len(b)-br.Len()) - start})
	}
	return spans, nil
}
