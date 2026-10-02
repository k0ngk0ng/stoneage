package battlepolicy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSafetensorsRoundTrip(t *testing.T) {
	a := recordedArtifactFixture(t)
	// Include negative zero and the smallest subnormal: a decimal-text roundtrip
	// must not be substituted for copying the actual float32 payload.
	for _, p := range a.Network.Parameters {
		if len(p.Values) < 2 {
			continue
		}
		p.Values[0] = math.Float32frombits(0x80000000)
		p.Values[1] = math.SmallestNonzeroFloat32
		break
	}
	a.WeightsDigest, _ = NetworkDigest(a.Network)
	legacy, _ := json.Marshal(a)
	raw, err := EncodeArtifact(a)
	if err != nil {
		t.Fatal(err)
	}
	n := binary.LittleEndian.Uint64(raw[:8])
	if n%8 != 0 || bytes.Contains(raw[8:8+n], []byte(`"values"`)) {
		t.Fatal("weights in JSON header")
	}
	got, err := DecodeArtifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(got)
	if !bytes.Equal(legacy, after) {
		t.Fatal("logical artifact or provenance changed")
	}
	for name, p := range a.Network.Parameters {
		for i, v := range p.Values {
			if math.Float32bits(v) != math.Float32bits(got.Network.Parameters[name].Values[i]) {
				t.Fatal("tensor changed", name, i)
			}
		}
	}
	again, err := EncodeArtifact(got)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("non-deterministic container", err)
	}
	old, err := DecodeArtifact(legacy)
	if err != nil || !reflect.DeepEqual(old, got) {
		t.Fatal("legacy compatibility", err)
	}
	path := filepath.Join(t.TempDir(), "model.safetensors")
	if err := SaveArtifact(path, a); err != nil {
		t.Fatal(err)
	}
	if err := SaveArtifact(path, a); err != nil {
		t.Fatal("same model not idempotent", err)
	}
	if _, err := LoadArtifact(path); err != nil {
		t.Fatal(err)
	}
	a.TrainingReport = strings.Repeat("2", 64)
	if err := SaveArtifact(path, a); err == nil {
		t.Fatal("overwrote different model")
	}
	b, _ := os.ReadFile(path)
	if !bytes.Equal(b, raw) {
		t.Fatal("published file changed")
	}
}

func TestSafetensorsRejectsCorruption(t *testing.T) {
	a := recordedArtifactFixture(t)
	valid, err := EncodeArtifact(a)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint64(valid[:8]))
	payload := valid[8+n:]
	for _, kind := range []string{"truncated", "huge_header", "trailing", "checksum", "nan", "duplicate", "shape", "overflow", "dtype", "overlap", "gap", "metadata", "unknown", "missing_tensor"} {
		t.Run(kind, func(t *testing.T) {
			b := append([]byte(nil), valid...)
			switch kind {
			case "truncated":
				b = b[:len(b)-1]
			case "huge_header":
				binary.LittleEndian.PutUint64(b[:8], 1<<63)
			case "trailing":
				b = append(b, 0)
			case "checksum":
				b[len(b)-1] ^= 1
			case "nan":
				binary.LittleEndian.PutUint32(b[8+n:], 0x7fc00000)
			default:
				var h map[string]json.RawMessage
				if err := json.Unmarshal(valid[8:8+n], &h); err != nil {
					t.Fatal(err)
				}
				var name string
				for k := range h {
					if k != "__metadata__" {
						name = k
						break
					}
				}
				var d tensorDescriptor
				json.Unmarshal(h[name], &d)
				switch kind {
				case "shape":
					d.Shape = []uint64{1}
				case "overflow":
					d.Shape = []uint64{1 << 63, 1 << 63}
				case "dtype":
					d.Dtype = "F64"
				case "overlap":
					h["extra"] = h[name]
				case "gap":
					d.Offsets[0] += 4
					d.Offsets[1] += 4
				case "metadata":
					delete(h, "__metadata__")
				case "missing_tensor":
					delete(h, name)
				}
				if kind == "shape" || kind == "overflow" || kind == "dtype" || kind == "gap" {
					h[name], _ = json.Marshal(d)
				}
				if kind == "unknown" {
					h[name] = json.RawMessage(`{"dtype":"F32","shape":[1,1],"data_offsets":[0,4],"unsafe":true}`)
				}
				header, _ := json.Marshal(h)
				if kind == "duplicate" {
					header = append([]byte(`{"__metadata__":{},`), header[1:]...)
				}
				b = binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
				b = append(b, header...)
				b = append(b, payload...)
			}
			if _, err := DecodeArtifact(b); err == nil {
				t.Fatal("corrupt container accepted")
			}
		})
	}
}
