package prodshadow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Store drives the production maintenance commands of one store directory. Every
// command is the real CLI; offline commands need the bridge stopped, exactly as
// production requires exclusive access.
type Store struct {
	B   *Binaries
	Dir string
}

// Run executes `zeros3 <args> -store Dir`. With live != "" the bridge binary is
// used and Z3_SHADOW_LIVE marks the listed chunks live (shadow-aware mark);
// otherwise the unmodified production binary runs.
func (s *Store) Run(live string, args ...string) ([]byte, int, error) {
	bin := s.B.Prod
	env := os.Environ()
	if live != "" {
		bin = s.B.Shadow
		env = append(env, "Z3_SHADOW_LIVE="+live)
	}
	cmd := exec.Command(bin, append(args, "-store", s.Dir)...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code, err = ee.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	if code != 0 {
		return out.Bytes(), code, fmt.Errorf("zeros3 %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), 0, nil
}

// Stats is the subset of `zeros3 stats -json` the experiment reports.
type Stats struct {
	ChunkStoreFileBytes       int64 `json:"chunk_store_file_bytes"`
	LooseChunkCount           int   `json:"loose_chunk_count"`
	LooseChunkFileBytes       int64 `json:"loose_chunk_file_bytes"`
	PackCount                 int   `json:"pack_count"`
	PackedChunkCount          int   `json:"packed_chunk_count"`
	PackFileBytes             int64 `json:"pack_file_bytes"`
	PackedRawRecords          int   `json:"packed_raw_records"`
	PackedCompressedRecs      int   `json:"packed_compressed_records"`
	PackedLogicalBytes        int64 `json:"packed_logical_bytes"`
	PackedStoredBytes         int64 `json:"packed_stored_bytes"`
	PackedLiveChunkCount      int   `json:"packed_live_chunk_count"`
	PackedDeadChunkCount      int   `json:"packed_dead_chunk_count"`
	UniqueReachableChunkBytes int64 `json:"unique_reachable_chunk_bytes"`
	ManifestFileBytes         int64 `json:"manifest_file_bytes"`
	JournalFileBytes          int64 `json:"journal_file_bytes"`
	ActualStoreFileBytes      int64 `json:"actual_store_file_bytes"`
	ReclaimableBytes          int64 `json:"reclaimable_bytes"`
	LogicalCurrentBytes       int64 `json:"logical_current_bytes"`
}

func (s *Store) Stats() (st Stats, err error) {
	out, _, err := s.Run("", "stats", "-json")
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(out, &st)
}

// Compact packs loose live chunks. With live set, the shadow-aware mark is used,
// so shadow chunks are packed too (production compaction packs only chunks some
// live root references).
func (s *Store) Compact(packMiB int, compress bool, live string) (map[string]any, error) {
	comp := "off"
	if compress {
		comp = "auto"
	}
	out, _, err := s.Run(live, "compact", "-json", "-pack-size-mib", strconv.Itoa(packMiB), "-compression", comp)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	return m, json.Unmarshal(out, &m)
}

// Verify runs `verify [-deep]` and returns the parsed JSON plus whether it passed.
func (s *Store) Verify(deep bool, live string) (map[string]any, bool, error) {
	args := []string{"verify", "-json"}
	if deep {
		args = append(args, "-deep")
	}
	out, code, err := s.Run(live, args...)
	m := map[string]any{}
	if len(out) > 0 {
		json.Unmarshal(out, &m)
	}
	if err != nil && code <= 0 {
		return m, false, err
	}
	return m, code == 0, nil
}

// GC runs gc (dry-run unless apply).
func (s *Store) GC(apply bool, live string) (map[string]any, error) {
	args := []string{"gc", "-json"}
	if apply {
		args = append(args, "-apply")
	}
	out, _, err := s.Run(live, args...)
	m := map[string]any{}
	if len(out) > 0 {
		json.Unmarshal(out, &m)
	}
	return m, err
}

func (s *Store) Repack(apply bool, packMiB int, live string) (map[string]any, error) {
	args := []string{"repack", "-json", "-pack-size-mib", strconv.Itoa(packMiB)}
	if apply {
		args = append(args, "-apply")
	}
	out, _, err := s.Run(live, args...)
	m := map[string]any{}
	if len(out) > 0 {
		json.Unmarshal(out, &m)
	}
	return m, err
}

// PackFiles lists pack ids by tier directory (hot: packs/, warm/cold: <tier>/packs).
func (s *Store) PackFiles() map[string][]string {
	out := map[string][]string{}
	for tier, dir := range map[string]string{"hot": filepath.Join(s.Dir, "packs"), "warm": filepath.Join(s.Dir, "tiers", "warm", "packs"), "cold": filepath.Join(s.Dir, "tiers", "cold", "packs")} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".pack") {
				out[tier] = append(out[tier], strings.TrimSuffix(e.Name(), ".pack"))
			}
		}
	}
	return out
}

// SpreadPacks places the store's packs round-robin over hot, warm and cold using
// the production `tier move` (which creates the tier roots); it returns the pack count per tier.
func (s *Store) SpreadPacks() (map[string]int, error) {
	ids := s.PackFiles()["hot"]
	sort.Strings(ids)
	for i, id := range ids {
		switch i % 3 {
		case 1:
			if err := s.TierMove("hot", "warm", id); err != nil {
				return nil, err
			}
		case 2:
			if err := s.TierMove("hot", "cold", id); err != nil {
				return nil, err
			}
		}
	}
	out := map[string]int{}
	for t, p := range s.PackFiles() {
		out[t] = len(p)
	}
	return out, nil
}

func (s *Store) TierInit(t string) error {
	_, _, err := s.Run("", "tier", "init", "-tier", t, "-json")
	return err
}

// TierMove moves the given packs between tiers (apply).
func (s *Store) TierMove(from, to string, packs ...string) error {
	args := []string{"tier", "move", "-from", from, "-to", to, "-apply", "-json"}
	for _, p := range packs {
		args = append(args, "-pack", p)
	}
	_, _, err := s.Run("", args...)
	return err
}
