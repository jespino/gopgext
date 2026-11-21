package pg

import (
	"unsafe"

	"gopgext/internal/core"
)

// ---- The current call -------------------------------------------------

// DefaultCollation is the database's default collation
// (DEFAULT_COLLATION_OID).
const DefaultCollation Oid = 100

// Collation returns the input collation of the SQL function being called:
// the collation PostgreSQL derived from its arguments (or a COLLATE
// clause), as PG_GET_COLLATION() in C. It is 0 when there is none
// (arguments of non-collatable types, conflicting collations) and outside
// calls. Pass it to ToLower, ToUpper and CompareStrings.
func Collation() Oid { return core.Collation() }

// ArgType returns the actual type of argument i (from 0) of the SQL
// function being called, as get_fn_expr_argtype: the type of a
// polymorphic or record argument even when it is NULL. 0 if unknown.
func ArgType(i int) Oid { return core.ArgType(i) }

// ---- Strings and hashing --------------------------------------------------

// ToLower returns s in lower case with the rules of collation coll, like
// lower() (str_tolower). A missing collation (0) raises PostgreSQL's
// "could not determine which collation to use" error, returned.
func ToLower(s string, coll Oid) (string, error) { return core.ToLower(s, coll) }

// ToUpper returns s in upper case with the rules of collation coll, like
// upper().
func ToUpper(s string, coll Oid) (string, error) { return core.ToUpper(s, coll) }

// CompareStrings compares a and b like text's comparison operators with
// collation coll (varstr_cmp): -1, 0 or 1.
func CompareStrings(a, b string, coll Oid) (int, error) { return core.CompareStrings(a, b, coll) }

//export hash_bytes
func hashBytes(k *byte, keylen int32) uint32

//export hash_bytes_extended
func hashBytesExtended(k *byte, keylen int32, seed uint64) uint64

// HashBytes is PostgreSQL's hash_any of b: what hash functions of hash
// operator classes return (as an int32), e.g. hashtext.
func HashBytes(b []byte) uint32 { return hashBytes(unsafe.SliceData(b), int32(len(b))) }

// HashBytesExtended is hash_any_extended: the 64-bit hash of extended hash
// functions (support function 2 of hash operator classes).
func HashBytesExtended(b []byte, seed uint64) uint64 {
	return hashBytesExtended(unsafe.SliceData(b), int32(len(b)), seed)
}

// ---- Values of any type ----------------------------------------------------

// FromBytes returns a value of the variable-length type typ (text, bytea,
// json, an extension's type stored like them) with contents b, e.g. the
// elements of an array built with NewAnyArrayDims. AnyElement.Bytes is
// the reverse.
func FromBytes(typ Oid, b []byte) AnyElement { return core.FromBytes(typ, b) }

// Input converts text to a value of type typ with type modifier typmod
// (-1: none), with the type's input function, as for a column of that
// type. A nil s runs the input function for NULL, which checks the
// constraints of a domain, and returns nil.
func Input(typ Oid, typmod int32, s *string) (*AnyElement, error) { return core.Input(typ, typmod, s) }

// NewAnyArrayDims returns an array of elements of type elem with the given
// dimensions and lower bounds (nil: all 1), xs being its elements in
// row-major order (nil: NULL). No dimensions make an empty array.
func NewAnyArrayDims(elem Oid, xs []*AnyElement, dims, lbounds []int) (AnyArray, error) {
	return core.NewAnyArrayDims(elem, xs, dims, lbounds)
}

// ---- Composite values (records) ---------------------------------------------

// IsComposite reports whether typ is a composite type, a domain over one,
// or record (type_is_rowtype).
func IsComposite(typ Oid) bool { return core.IsComposite(typ) }

// Columns returns the columns of the composite type typ, including dropped
// ones (Dropped): for a domain over a composite type, those of its base
// type; for an anonymous record, typmod identifies it (see
// AnyElement.RowType), else it is -1.
func Columns(typ Oid, typmod int32) ([]Column, error) { return core.Columns(typ, typmod) }

// NewRecord returns a value of the composite type typ (typmod as for
// Columns) from the values of its columns, one per column of Columns (nil:
// NULL; dropped columns are NULL). The constraints of a domain over a
// composite type are checked.
func NewRecord(typ Oid, typmod int32, values []*AnyElement) (AnyElement, error) {
	return core.NewRecord(typ, typmod, values)
}
