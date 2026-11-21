package core

import "unsafe"

// Collation returns the input collation of the current call
// (FunctionCallInfoBaseData.fncollation, checked in values.c).
func Collation() Oid {
	if FCInfo == 0 {
		return 0
	}
	return *(*Oid)(unsafe.Pointer(FCInfo + 24))
}

//export gopgext_fn_argtype
func fnArgType(fcinfo uintptr, i int64, out *uint32) uintptr

// ArgType returns the actual type of argument i of the current call (0 if
// unknown).
func ArgType(i int) Oid {
	var oid uint32
	Check(fnArgType(FCInfo, int64(i), &oid))
	return Oid(oid)
}

//export gopgext_varlena_get
func varlenaGet(d uint64, ptr *unsafe.Pointer, n *int64) uintptr

//export gopgext_varlena_new
func varlenaNew(p *byte, n int64, out *uint64) uintptr

// Bytes returns the contents of a value of a variable-length type (text,
// bytea, json...), detoasted, not copied: valid during the call.
func (a AnyElement) Bytes() []byte {
	var p unsafe.Pointer
	var n int64
	Check(varlenaGet(a.d, &p, &n))
	return unsafe.Slice((*byte)(p), n)
}

// FromBytes returns a value of variable-length type typ with contents b
// (copied into PostgreSQL memory).
func FromBytes(typ Oid, b []byte) AnyElement {
	var d uint64
	Check(varlenaNew(unsafe.SliceData(b), int64(len(b)), &d))
	return AnyElement{d, uint32(typ)}
}

//export gopgext_str_case
func strCase(p *byte, n int64, coll uint32, upper bool, out **byte, outlen *int64) uintptr

func strCaseGo(s string, coll Oid, upper bool) (string, error) {
	var p *byte
	var n int64
	if ed := strCase(StringData(s), int64(len(s)), uint32(coll), upper, &p, &n); ed != 0 {
		return "", FromErrorData(ed)
	}
	return string(unsafe.Slice(p, n)), nil
}

// ToLower and ToUpper are lower() and upper() with collation coll.
func ToLower(s string, coll Oid) (string, error) { return strCaseGo(s, coll, false) }
func ToUpper(s string, coll Oid) (string, error) { return strCaseGo(s, coll, true) }

//export gopgext_str_cmp
func strCmp(a *byte, alen int64, b *byte, blen int64, coll uint32, out *int64) uintptr

// CompareStrings compares a and b like text's comparison operators with
// collation coll: -1, 0 or 1.
func CompareStrings(a, b string, coll Oid) (int, error) {
	var r int64
	if ed := strCmp(StringData(a), int64(len(a)), StringData(b), int64(len(b)), uint32(coll), &r); ed != 0 {
		return 0, FromErrorData(ed)
	}
	return int(r), nil
}

// ---- Composite values ---------------------------------------------------

// Column is a column of a row: of a composite type, or of the row result
// of a call (pg.Column).
type Column struct {
	Name     string
	Type     Oid
	Typmod   int32  // -1: none
	TypeName string // with the type modifier, e.g. "character varying(10)" (result columns)
	Dropped  bool   // a dropped column, always NULL (composite types)
}

type ccolumn struct {
	name    *byte
	typ     uint32
	typmod  int32
	dropped bool
}

//export gopgext_type_is_rowtype
func typeIsRowtype(typ uint32, out *bool) uintptr

// IsComposite reports whether typ is a composite type, a domain over one,
// or record.
func IsComposite(typ Oid) bool {
	var r bool
	Check(typeIsRowtype(uint32(typ), &r))
	return r
}

//export gopgext_record_columns
func recordColumns(typ uint32, typmod int32, n *int64, out **ccolumn) uintptr

// Columns returns the columns of a composite type.
func Columns(typ Oid, typmod int32) ([]Column, error) {
	var n int64
	var p *ccolumn
	if ed := recordColumns(uint32(typ), typmod, &n, &p); ed != 0 {
		return nil, FromErrorData(ed)
	}
	cs := unsafe.Slice(p, n)
	out := make([]Column, n)
	for i, c := range cs {
		out[i] = Column{Name: GoString(c.name), Type: Oid(c.typ), Typmod: c.typmod, Dropped: c.dropped}
	}
	return out, nil
}

//export gopgext_record_type
func recordType(d uint64, typ *uint32, typmod *int32) uintptr

// RowType returns the type and typmod of a composite value, read from the
// value itself (so it works for anonymous records).
func (a AnyElement) RowType() (Oid, int32) {
	var typ uint32
	var typmod int32
	Check(recordType(a.d, &typ, &typmod))
	return Oid(typ), typmod
}

//export gopgext_record_fields
func recordFields(d uint64, n *int64, values, nulls, types *unsafe.Pointer) uintptr

// Fields returns the values of the columns of a composite value, nil for
// NULL (and dropped) columns.
func (a AnyElement) Fields() []*AnyElement {
	var n int64
	var values, nulls, types unsafe.Pointer
	Check(recordFields(a.d, &n, &values, &nulls, &types))
	ds := unsafe.Slice((*uint64)(values), n)
	ns := unsafe.Slice((*bool)(nulls), n)
	ts := unsafe.Slice((*uint32)(types), n)
	out := make([]*AnyElement, n)
	for i := range out {
		if !ns[i] {
			out[i] = &AnyElement{ds[i], ts[i]}
		}
	}
	return out
}

//export gopgext_record_new
func recordNew(typ uint32, typmod int32, n int64, values *uint64, nulls *bool, out *uint64) uintptr

// NewRecord returns a composite value of type typ from the values of its
// columns (nil: NULL).
func NewRecord(typ Oid, typmod int32, values []*AnyElement) (AnyElement, error) {
	ds := make([]uint64, len(values)+1)
	ns := make([]bool, len(values)+1)
	for i, v := range values {
		if v == nil {
			ns[i] = true
		} else {
			ds[i] = v.d
		}
	}
	var d uint64
	if ed := recordNew(uint32(typ), typmod, int64(len(values)), &ds[0], &ns[0], &d); ed != 0 {
		return AnyElement{}, FromErrorData(ed)
	}
	return AnyElement{d, uint32(typ)}, nil
}

//export gopgext_input
func input(typ uint32, typmod int32, s *byte, out *uint64, isnull *bool) uintptr

// Input runs the input function of type typ on s with typmod; nil s runs
// it for NULL (domain checks) and returns nil.
func Input(typ Oid, typmod int32, s *string) (*AnyElement, error) {
	var p *byte
	if s != nil {
		p = CBytes(*s)
	}
	var d uint64
	var null bool
	if ed := input(uint32(typ), typmod, p, &d, &null); ed != 0 {
		return nil, FromErrorData(ed)
	}
	if null {
		return nil, nil
	}
	return &AnyElement{d, uint32(typ)}, nil
}

// ---- Multi-dimensional arrays -------------------------------------------

//export gopgext_array_bounds
func arrayBounds(d uint64, ndim *int64, dims, lbs *int32) uintptr

// LowerBounds returns the lower bound of each dimension (none for an
// empty array).
func (a AnyArray) LowerBounds() []int {
	var n int64
	var dims, lbs [6]int32 // MAXDIM
	Check(arrayBounds(a.d, &n, &dims[0], &lbs[0]))
	out := make([]int, n)
	for i := range out {
		out[i] = int(lbs[i])
	}
	return out
}

//export gopgext_array_new_md
func arrayNewMD(elem uint32, ndim int64, dims, lbs *int32, elems *uint64, nulls *bool, out *uint64) uintptr

// NewAnyArrayDims returns an array of elements of type elem with the given
// dimensions and lower bounds (nil: all 1); xs are its elements in row-major
// order. No dimensions make an empty array.
func NewAnyArrayDims(elem Oid, xs []*AnyElement, dims, lbounds []int) (AnyArray, error) {
	n := 1
	cd := make([]int32, len(dims)+1)
	cl := make([]int32, len(dims)+1)
	for i, d := range dims {
		n *= d
		cd[i] = int32(d)
		cl[i] = 1
		if lbounds != nil {
			cl[i] = int32(lbounds[i])
		}
	}
	if len(dims) == 0 {
		n = 0
	}
	if n != len(xs) || (lbounds != nil && len(lbounds) != len(dims)) {
		return AnyArray{}, &Error{Code: "2202E", Message: "array dimensions don't match the number of elements"}
	}
	ds := make([]uint64, len(xs)+1)
	ns := make([]bool, len(xs)+1)
	for i, x := range xs {
		if x == nil {
			ns[i] = true
		} else {
			ds[i] = x.d
		}
	}
	var d uint64
	if ed := arrayNewMD(uint32(elem), int64(len(dims)), &cd[0], &cl[0], &ds[0], &ns[0], &d); ed != 0 {
		return AnyArray{}, FromErrorData(ed)
	}
	return AnyArray{d, uint32(elem)}, nil
}
