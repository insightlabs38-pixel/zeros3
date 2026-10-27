package main

import (
	"bytes"
	"fmt"

	"zeros3-z3/dexz3"
)

func init() {
	cmds["slotcmp"] = func(args []string) error {
		f, b, err := loadDex(args[0])
		if err != nil {
			return err
		}
		w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			return err
		}
		m := append([]byte(nil), b...)
		dexz3.Mask(m, w.Slots, nil)
		f2, err := dexz3.Parse(bytes.NewReader(m), int64(len(m)), dexz3.Limits{})
		if err != nil {
			return err
		}
		w2, err := dexz3.Walk(f2, m, dexz3.WalkOpts{Masked: true, Offsets: true, Debug: true})
		if err != nil {
			return err
		}
		fmt.Println(len(w.Slots), len(w2.Slots))
		for i := range w.Slots {
			if i >= len(w2.Slots) {
				break
			}
			a, c := w.Slots[i], w2.Slots[i]
			if a.Off != c.Off || a.Len != c.Len || a.Enc != c.Enc || a.Space != c.Space || a.Kind != c.Kind {
				fmt.Printf("first diff at %d: %+v vs %+v\n", i, a, c)
				return nil
			}
		}
		return nil
	}
}
