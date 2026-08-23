package classify

// A minimal FlatBuffers reader, sized to one job: pulling named constant
// tensors out of the pinned YAMNet .tflite file.
//
// This is deliberately not a TFLite interpreter and not a general FlatBuffers
// library. The model file is pinned by SHA-256 (model.go), so the only schema
// this will ever read is the one that file was built with; the reader walks
// Model → subgraphs[0] → tensors and Model → buffers, and nothing else. The
// alternative — depending on the official flatbuffers package plus generated
// schema bindings — buys generality this application has no use for, at the
// cost of a dependency for what is ~150 lines of offset arithmetic.
//
// FlatBuffers layout, as much of it as is used here:
//
//   - The file starts with a uint32 offset to the root table, then the
//     4-byte file identifier ("TFL3").
//   - A table starts with an int32 pointing *back* to its vtable. The vtable
//     holds uint16 offsets, one per field id, each relative to the table
//     start; 0 means the field is absent.
//   - Vectors and strings are a uint32 length followed by the elements.
//   - Offset-typed fields store a uint32 pointing forward from the field's
//     own position.
//
// Field ids below are from tensorflow/lite/schema/schema.fbs, which is
// append-only by policy — ids never renumber, which is what makes hardcoding
// them sound.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type fbuf struct{ b []byte }

func (f fbuf) u16(pos int) uint16 { return binary.LittleEndian.Uint16(f.b[pos:]) }
func (f fbuf) u32(pos int) uint32 { return binary.LittleEndian.Uint32(f.b[pos:]) }
func (f fbuf) i32(pos int) int32  { return int32(f.u32(pos)) }

// table resolves the vtable for the table at pos.
func (f fbuf) table(pos int) fbTable {
	vt := pos - int(f.i32(pos))
	return fbTable{f: f, pos: pos, vtable: vt, vsize: int(f.u16(vt))}
}

type fbTable struct {
	f      fbuf
	pos    int
	vtable int
	vsize  int
}

// fieldPos returns the absolute position of field id, or 0 when absent.
func (t fbTable) fieldPos(id int) int {
	slot := 4 + 2*id
	if slot+2 > t.vsize {
		return 0
	}
	off := int(t.f.u16(t.vtable + slot))
	if off == 0 {
		return 0
	}
	return t.pos + off
}

// indirect follows a uoffset field to the table/vector/string it points at.
func (t fbTable) indirect(id int) int {
	p := t.fieldPos(id)
	if p == 0 {
		return 0
	}
	return p + int(t.f.u32(p))
}

func (t fbTable) subTable(id int) (fbTable, bool) {
	p := t.indirect(id)
	if p == 0 {
		return fbTable{}, false
	}
	return t.f.table(p), true
}

// scalar fields, with schema defaults when absent.
func (t fbTable) byteField(id int, def byte) byte {
	p := t.fieldPos(id)
	if p == 0 {
		return def
	}
	return t.f.b[p]
}

func (t fbTable) u32Field(id int, def uint32) uint32 {
	p := t.fieldPos(id)
	if p == 0 {
		return def
	}
	return t.f.u32(p)
}

func (t fbTable) i32Field(id int, def int32) int32 {
	p := t.fieldPos(id)
	if p == 0 {
		return def
	}
	return t.f.i32(p)
}

// vec returns (element area, length) for a vector field.
func (t fbTable) vec(id int) (pos, n int) {
	p := t.indirect(id)
	if p == 0 {
		return 0, 0
	}
	return p + 4, int(t.f.u32(p))
}

func (t fbTable) str(id int) string {
	pos, n := t.vec(id)
	if pos == 0 {
		return ""
	}
	return string(t.f.b[pos : pos+n])
}

// tableAt returns the i-th table of a vector-of-tables field.
func (t fbTable) tableAt(id, i int) (fbTable, bool) {
	pos, n := t.vec(id)
	if pos == 0 || i >= n {
		return fbTable{}, false
	}
	elem := pos + 4*i
	return t.f.table(elem + int(t.f.u32(elem))), true
}

func (t fbTable) i32Vec(id int) []int32 {
	pos, n := t.vec(id)
	out := make([]int32, n)
	for i := range out {
		out[i] = t.f.i32(pos + 4*i)
	}
	return out
}

func (t fbTable) f32Vec(id int) []float32 {
	pos, n := t.vec(id)
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(t.f.u32(pos + 4*i))
	}
	return out
}

func (t fbTable) i64Vec(id int) []int64 {
	pos, n := t.vec(id)
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(binary.LittleEndian.Uint64(t.f.b[pos+8*i:]))
	}
	return out
}

// --- the TFLite view -------------------------------------------------------

// tensorInfo is one entry of subgraphs[0].tensors, with its constant data
// resolved (nil for activations, whose buffers are empty).
type tensorInfo struct {
	name  string
	shape []int32
	// dtype is the schema's TensorType: 0 float32, 2 int32, 9 int8. Others
	// exist in the file (the RFFT intermediates) but are never extracted.
	dtype byte
	data  []byte

	scales   []float32
	zeros    []int64
	quantDim int32
}

// schema field ids. Append-only in schema.fbs; see the package comment.
const (
	fModelSubgraphs = 2
	fModelBuffers   = 4

	fSubgraphTensors = 0

	fTensorShape = 0
	fTensorType  = 1
	fTensorBuf   = 2
	fTensorName  = 3
	fTensorQuant = 4

	fQuantScale = 2
	fQuantZero  = 3
	fQuantDim   = 6

	fBufferData = 0
)

// readTensors parses a .tflite file into its tensor list.
func readTensors(raw []byte) ([]tensorInfo, error) {
	if len(raw) < 8 {
		return nil, errors.New("not a flatbuffer: too short")
	}
	f := fbuf{b: raw}
	if string(raw[4:8]) != "TFL3" {
		return nil, fmt.Errorf("not a TFLite model: identifier %q", raw[4:8])
	}
	model := f.table(int(f.u32(0)))

	// Buffers first, so tensors can resolve their data as they are read.
	bufsPos, bufsN := model.vec(fModelBuffers)
	buffers := make([][]byte, bufsN)
	for i := range buffers {
		elem := bufsPos + 4*i
		buf := f.table(elem + int(f.u32(elem)))
		if pos, n := buf.vec(fBufferData); pos != 0 && n > 0 {
			buffers[i] = raw[pos : pos+n]
		}
	}

	graph, ok := model.tableAt(fModelSubgraphs, 0)
	if !ok {
		return nil, errors.New("model has no subgraph")
	}
	_, tensorsN := graph.vec(fSubgraphTensors)
	out := make([]tensorInfo, 0, tensorsN)
	for i := 0; i < tensorsN; i++ {
		t, _ := graph.tableAt(fSubgraphTensors, i)
		info := tensorInfo{
			name:  t.str(fTensorName),
			shape: t.i32Vec(fTensorShape),
			dtype: t.byteField(fTensorType, 0),
		}
		if b := int(t.u32Field(fTensorBuf, 0)); b > 0 && b < len(buffers) {
			info.data = buffers[b]
		}
		if q, ok := t.subTable(fTensorQuant); ok {
			info.scales = q.f32Vec(fQuantScale)
			info.zeros = q.i64Vec(fQuantZero)
			info.quantDim = q.i32Field(fQuantDim, 0)
		}
		out = append(out, info)
	}
	return out, nil
}

func (t *tensorInfo) elems() int {
	n := 1
	for _, d := range t.shape {
		n *= int(d)
	}
	return n
}

// f32 decodes a float32 constant tensor.
func (t *tensorInfo) f32() ([]float32, error) {
	if t.dtype != 0 {
		return nil, fmt.Errorf("tensor %q: dtype %d, want float32", t.name, t.dtype)
	}
	n := t.elems()
	if len(t.data) != 4*n {
		return nil, fmt.Errorf("tensor %q: %d data bytes for %d elems", t.name, len(t.data), n)
	}
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(t.data[4*i:]))
	}
	return out, nil
}
