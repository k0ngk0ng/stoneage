package battlepolicy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

const maxModelBytes = 128 << 20
const maxTensorHeaderBytes = 16 << 20
const tensorFormat = "stoneage-policy-v1"

type tensorDescriptor struct {
	Dtype   string   `json:"dtype"`
	Shape   []uint64 `json:"shape"`
	Offsets []uint64 `json:"data_offsets"`
}

// EncodeArtifact stores F32 tensors in standard safetensors little-endian
// buffers. The JSON metadata contains the architecture and provenance only.
// Logical artifact/weight identities deliberately keep their existing canonical
// representation, so changing the container never changes policy identity.
func EncodeArtifact(a Artifact) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	manifest := a
	manifest.Network = &battlenet.Model[float32]{Config: a.Network.Config}
	metadata, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	header := map[string]any{"__metadata__": map[string]string{"format": tensorFormat, "stoneage": string(metadata)}}
	names := make([]string, 0, len(a.Network.Parameters))
	for name := range a.Network.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	var data []byte
	for _, name := range names {
		p := a.Network.Parameters[name]
		start := len(data)
		if len(p.Values) > (maxModelBytes-start)/4 {
			return nil, fmt.Errorf("model file exceeds limit")
		}
		for _, v := range p.Values {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(v))
		}
		header[name] = tensorDescriptor{"F32", []uint64{uint64(p.Rows), uint64(p.Cols)}, []uint64{uint64(start), uint64(len(data))}}
	}
	h, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	for len(h)%8 != 0 {
		h = append(h, ' ')
	}
	if len(h) > maxTensorHeaderBytes || len(h)+len(data)+8 > maxModelBytes {
		return nil, fmt.Errorf("model file exceeds limit")
	}
	b := binary.LittleEndian.AppendUint64(nil, uint64(len(h)))
	b = append(b, h...)
	return append(b, data...), nil
}

func decodeTensorArtifact(b []byte) (Artifact, error) {
	var a Artifact
	if len(b) < 8 {
		return a, fmt.Errorf("truncated safetensors length")
	}
	n := binary.LittleEndian.Uint64(b[:8])
	if n < 2 || n > maxTensorHeaderBytes || n > uint64(len(b)-8) {
		return a, fmt.Errorf("invalid safetensors header length")
	}
	h, data := b[8:8+int(n)], b[8+int(n):]
	if h[0] != '{' {
		return a, fmt.Errorf("invalid safetensors header")
	}
	var header map[string]json.RawMessage
	if err := strictTensorJSON(h, &header); err != nil {
		return a, err
	}
	if len(header) < 2 || len(header) > 1024 {
		return a, fmt.Errorf("invalid tensor count")
	}
	var metadata map[string]string
	if err := strictTensorJSON(header["__metadata__"], &metadata); err != nil {
		return a, err
	}
	if metadata["format"] != tensorFormat || len(metadata) != 2 {
		return a, fmt.Errorf("unsupported safetensors metadata")
	}
	if err := strictTensorJSON([]byte(metadata["stoneage"]), &a); err != nil {
		return a, err
	}
	if a.Network == nil || len(a.Network.Parameters) != 0 {
		return a, fmt.Errorf("safetensors metadata must not contain weights")
	}
	if err := a.Network.Config.Validate(); err != nil {
		return a, err
	}
	a.Network.Parameters = map[string]battlenet.Parameter[float32]{}
	type entry struct {
		name   string
		tensor tensorDescriptor
	}
	entries := make([]entry, 0, len(header)-1)
	for name, raw := range header {
		if name == "__metadata__" {
			continue
		}
		var d tensorDescriptor
		if err := strictTensorJSON(raw, &d); err != nil {
			return a, err
		}
		if d.Dtype != "F32" || len(d.Shape) != 2 || len(d.Offsets) != 2 || d.Shape[0] == 0 || d.Shape[1] == 0 || d.Offsets[0] > d.Offsets[1] || d.Offsets[1] > uint64(len(data)) {
			return a, fmt.Errorf("invalid tensor %q", name)
		}
		// Division bounds the product before multiplication or int conversion.
		if d.Shape[1] > uint64(len(data))/4 || d.Shape[0] > uint64(len(data))/4/d.Shape[1] || d.Shape[0]*d.Shape[1]*4 != d.Offsets[1]-d.Offsets[0] {
			return a, fmt.Errorf("invalid tensor shape %q", name)
		}
		entries = append(entries, entry{name, d})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].tensor.Offsets[0] < entries[j].tensor.Offsets[0] })
	var end uint64
	for _, e := range entries {
		d := e.tensor
		if d.Offsets[0] != end {
			return a, fmt.Errorf("overlapping or non-contiguous tensor data")
		}
		end = d.Offsets[1]
		values := make([]float32, int(d.Shape[0]*d.Shape[1]))
		raw := data[d.Offsets[0]:end]
		for i := range values {
			values[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		a.Network.Parameters[e.name] = battlenet.Parameter[float32]{Rows: int(d.Shape[0]), Cols: int(d.Shape[1]), Values: values}
	}
	if end != uint64(len(data)) {
		return a, fmt.Errorf("trailing tensor data")
	}
	return a, a.Validate()
}

// JSON's default decoder accepts duplicate keys. A tensor header must have a
// single unambiguous interpretation for Go and independent safetensors readers.
func strictTensorJSON(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("model metadata nesting exceeds limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return fmt.Errorf("unexpected delimiter")
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid metadata key")
				}
				seen[key] = true
			}
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing model metadata")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

// SaveArtifact publishes a complete file without replacing an existing model.
// Explicit .json output is retained for old clients; all default exports use
// .safetensors. No checkpoint or source model is modified during conversion.
func SaveArtifact(path string, a Artifact) error {
	var b []byte
	var err error
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if err = a.Validate(); err != nil {
			return err
		}
		b, err = json.Marshal(a)
		b = append(b, '\n')
	} else {
		b, err = EncodeArtifact(a)
	}
	if err != nil {
		return err
	}
	if len(b) > maxModelBytes {
		return fmt.Errorf("model file exceeds limit")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".model-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || st.Size() != int64(len(b)) {
			return fmt.Errorf("refusing to overwrite existing model")
		}
		old, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(old, b) {
			return fmt.Errorf("refusing to overwrite different model")
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
