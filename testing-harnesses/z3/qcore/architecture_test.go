package qcore

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"testing"
)

type fixedOracleVector struct {
	Name      string
	Type      Type
	Row       int
	Values    []byte
	Weights   []byte
	StrictSHA string
	FusedSHA  string
}

func fixedFloats(data []byte) []float32 {
	if data == nil {
		return nil
	}
	values := make([]float32, len(data)/4)
	for i := range values {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return values
}

func fixedVectorHash(v fixedOracleVector, ar Arith) (string, error) {
	if len(v.Values)%4 != 0 || len(v.Weights)%4 != 0 {
		return "", fmt.Errorf("invalid binary input for %s", v.Name)
	}
	x, w := fixedFloats(v.Values), fixedFloats(v.Weights)
	size, err := OutLen(v.Type, len(x), v.Row)
	if err != nil {
		return "", err
	}
	data := make([]byte, size)
	err = Quantize(data, v.Type, x, v.Row, w, ar)
	if err != nil {
		return "error:" + err.Error(), nil
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// TestFixedArchitectureInputs reuses identical IEEE-754 bytes across runners.
// The canonical file records amd64 output for each profile as a comparison.
func TestFixedArchitectureInputs(t *testing.T) {
	path := os.Getenv("QCORE_FIXED_VECTORS")
	if path == "" {
		t.Skip("set QCORE_FIXED_VECTORS to a scratch input")
	}
	if os.Getenv("QCORE_FIXED_WRITE") == "1" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		var pack []fixedOracleVector
		for _, v := range vectors() {
			row := fixedOracleVector{Name: v.name, Type: v.t, Row: v.npr, Values: f32bytes(v.x)}
			if v.qw != nil {
				row.Weights = f32bytes(v.qw)
			}
			pack = append(pack, row)
		}
		if path := os.Getenv("QCORE_FIXED_REAL_INPUT"); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, typ := range []Type{Q2_K, Q4_K, Q5_K, Q6_K, IQ4_XS, IQ3_S} {
				pack = append(pack, fixedOracleVector{Name: typ.String() + "/pythia-f16-excerpt", Type: typ, Row: 768, Values: data})
			}
		}
		for i, row := range pack {
			pack[i].StrictSHA, err = fixedVectorHash(row, Strict)
			if err != nil {
				t.Fatal(err)
			}
			if fusedPath(row.Type, row.Weights != nil) {
				pack[i].FusedSHA, err = fixedVectorHash(row, GCCFMA)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		err = json.NewEncoder(f).Encode(pack)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pack []fixedOracleVector
	if err := json.Unmarshal(raw, &pack); err != nil || len(pack) == 0 {
		t.Fatal("empty or invalid fixed vectors", err)
	}
	t.Logf("%s identical-input fixture SHA-256 %x with %d vectors", runtime.GOARCH, sha256.Sum256(raw), len(pack))
	checked := 0
	for _, v := range pack {
		for _, profile := range []struct {
			name  string
			arith Arith
			want  string
		}{{"strict", Strict, v.StrictSHA}, {"gcc-fma", GCCFMA, v.FusedSHA}} {
			if profile.want == "" {
				continue
			}
			got, err := fixedVectorHash(v, profile.arith)
			if err != nil || got != profile.want {
				t.Errorf("%s %s exact output changed: %s versus %s (%v)", v.Name, profile.name, got, profile.want, err)
			}
			checked++
		}
	}
	t.Logf("checked %d profile and input pairs", checked)
}
