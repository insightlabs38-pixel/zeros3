package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Publish a fresh research export. Preserve the complete preceding export;
// changing a corpus must not silently leave uncharged orphan objects in it.
func exportStore(dir string, objects map[[32]byte][]byte, m manifest) error {
	parent := filepath.Dir(dir)
	if e := os.MkdirAll(parent, 0755); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(parent, "."+filepath.Base(dir)+"-staging-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for h, w := range objects {
		if e := os.WriteFile(filepath.Join(stage, hex.EncodeToString(h[:])+".delta"), w, 0644); e != nil {
			return e
		}
	}
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(stage, "roots.json"), b, 0644); e != nil {
		return e
	}
	previous := ""
	if _, e = os.Stat(dir); e == nil {
		previous, e = os.MkdirTemp(parent, filepath.Base(dir)+"-previous-")
		if e != nil {
			return e
		}
		if e = os.Remove(previous); e != nil {
			return e
		}
		if e = os.Rename(dir, previous); e != nil {
			os.Remove(previous)
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if e = os.Rename(stage, dir); e != nil {
		if previous != "" {
			if restore := os.Rename(previous, dir); restore != nil {
				return fmt.Errorf("publish: %v; restore: %v", e, restore)
			}
		}
		return e
	}
	return nil
}
