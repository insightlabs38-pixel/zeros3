package kinds

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"zeros3-z3/clrz3"
	"zeros3-z3/repz3"
)

// CLR is the Z3-33 lab kind. The representation is
//
//	Recipe   the clrz3 meta record: the exact header regions (PE, CLI header, metadata root, table-stream header) and the identity parameters
//	Members  index nodes, STORED: each is a run of 32-byte digests of component groups
//
// The component groups (a slice of the masked assembly payload followed by the reference streams of the
// slots that start inside it) hang off the nodes (VIA, named only inside the stored nodes), so one
// changed group changes one group, one node and the recipe, and liveness is ordinary
// reachability: Expand reads the nodes. Nothing is a closure and nothing needs the producer; the
// decoder is clrz3.Decode over the serialized bytes. Any assembly the parser refuses stays CDC.

// clrNodeFan is the mean number of groups per index node (cut where a digest mod fan is zero).
const clrNodeFan = 64

// MaxCLRGroups bounds one representation (a 512 MiB assembly in 1 KiB groups).
const maxCLRGroups = 1 << 20

// EncodeCLR analyses one assembly; an error wrapping clrz3.ErrFormat, ErrUnsupported or ErrLimit means
// "use CDC".
func EncodeCLR(data []byte, cfg clrz3.Config) (*Bundle, clrz3.Stats, error) {
	f, err := clrz3.Parse(bytes.NewReader(data), int64(len(data)), clrz3.Limits{})
	if err != nil {
		return nil, clrz3.Stats{}, err
	}
	rep, err := clrz3.Encode(f, data, cfg)
	if err != nil {
		return nil, clrz3.Stats{}, err
	}
	out := &repz3.Rep{Kind: repz3.KindCLR, Size: int64(len(data)), Recipe: rep.Meta}
	var objs [][]byte
	var cur []byte
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(cur), Size: int64(len(cur))})
		objs = append(objs, cur)
		cur = nil
	}
	n := 0
	for _, c := range rep.Comps {
		id := sha256.Sum256(c)
		objs = append(objs, c)
		cur = append(cur, id[:]...)
		n++
		if (int(id[0])<<8|int(id[1]))%clrNodeFan == 0 || n >= 4*clrNodeFan {
			flush()
			n = 0
		}
	}
	flush()
	b, err := newBundle(out, objs)
	return b, rep.Stats, err
}

type clrDecoder struct{}

// clrGroups reads the member nodes and returns the digests of the component groups in order.
func clrGroups(c *repz3.Call) ([]repz3.SHA, error) {
	var ids []repz3.SHA
	for i := range c.Rep.Members {
		m := &c.Rep.Members[i]
		if m.Role != repz3.Stored {
			return nil, fmt.Errorf("%w: CLR member %d is not a stored node", repz3.ErrCorrupt, i)
		}
		nb, err := c.Get(m.SHA)
		if err != nil {
			return nil, err
		}
		if len(nb) == 0 || len(nb)%32 != 0 || len(ids)+len(nb)/32 > maxCLRGroups {
			return nil, fmt.Errorf("%w: CLR index node %d of %d bytes", repz3.ErrCorrupt, i, len(nb))
		}
		for o := 0; o < len(nb); o += 32 {
			var id repz3.SHA
			copy(id[:], nb[o:])
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (clrDecoder) Decode(c *repz3.Call) error {
	ids, err := clrGroups(c)
	if err != nil {
		return err
	}
	comps := make([][]byte, len(ids))
	for i, id := range ids {
		if comps[i], err = c.Get(id); err != nil {
			return err
		}
	}
	out, err := clrz3.Decode(c.Rep.Recipe, comps)
	if err != nil {
		if errors.Is(err, clrz3.ErrFormat) || errors.Is(err, clrz3.ErrUnsupported) || errors.Is(err, clrz3.ErrLimit) {
			return fmt.Errorf("%w: %v", repz3.ErrCorrupt, err)
		}
		return err
	}
	_, err = c.W.Write(out)
	return err
}

// Expand names the component groups (the members are the nodes; the groups are only in them).
func (clrDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	ids, err := clrGroups(c)
	if err != nil {
		return err
	}
	for _, id := range ids {
		emit(id)
	}
	return nil
}
