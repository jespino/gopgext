// isn is a Go port of PostgreSQL's contrib/isn (version 1.3): data types for
// international product numbering standards (EAN13, UPC, ISBN, ISMN, ISSN).
//
// The SQL objects (types, operators, operator classes, casts) are upstream's
// own install scripts, in sql/. Those reference 15 C functions, which are
// implemented here: they're exported with upstream's C symbol names
// (symbol=...) and declared by the scripts (manual). Everything else is SQL
// on top of PostgreSQL's int8 functions, since the types are LIKE int8.
//
// Ported from PostgreSQL 19's contrib/isn/isn.c. The code keeps the
// original's buffer-and-index structure on purpose: hyphenation and check
// digit handling are subtle, and the port must produce exactly the same
// strings.
package main

import "gopgext/pg"

// isn.weak: accept input with invalid check digits (flagged as invalid).
var weak = pg.NewBoolSetting("weak", false, "Accept input with invalid ISN check digits.")

func main() {}

const maxEAN13Len = 18

type isnType int

const (
	typeInvalid isnType = iota
	typeAny
	typeEAN13
	typeISBN
	typeISMN
	typeISSN
	typeUPC
)

var isnNames = [...]string{"EAN13/UPC/ISxN", "EAN13/UPC/ISxN", "EAN13", "ISBN", "ISMN", "ISSN", "UPC"}

// cstr makes a NUL-terminated byte string (table entries).
func cstr(s string) []byte { return append([]byte(s), 0) }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func asciiUpper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// cString returns the NUL-terminated string at b[i:].
func cString(b []byte, i int) string {
	j := i
	for j < len(b) && b[j] != 0 {
		j++
	}
	return string(b[i:j])
}

// strncmpEq is strncmp(a, b + i, n) == 0 (a is a Go string).
func strncmpEq(a string, b []byte, i int, n uint32) bool {
	for k := 0; uint32(k) < n; k++ {
		var ca, cb byte
		if k < len(a) {
			ca = a[k]
		}
		if i+k < len(b) {
			cb = b[i+k]
		}
		if ca != cb {
			return false
		}
		if ca == 0 {
			return true
		}
	}
	return true
}

// ---- Formatting and conversion routines --------------------------------

// dehyphenate copies the digits of b[in:] to b[out:] (in place, out <= in).
func dehyphenate(b []byte, out, in int) uint32 {
	var ret uint32
	for b[in] != 0 {
		if isDigit(b[in]) {
			b[out] = b[in]
			out++
			ret++
		}
		in++
	}
	b[out] = 0
	return ret
}

// hyphenate tries to hyphenate the digits at b[in:] into b[out:] (in place)
// using the hyphenation range table. It returns the number of characters
// actually hyphenated (0 if no range matched).
func hyphenate(b []byte, out, in int, table [][2][]byte, index *[10][2]uint32) uint32 {
	var ret uint32

	// Just compress the string if no further hyphenation is required.
	if table == nil || index == nil {
		for b[in] != 0 {
			b[out] = b[in]
			out++
			in++
			ret++
		}
		b[out] = 0
		return ret + 1
	}

	// Binary search for the range, with the original's unsigned arithmetic
	// (lower starts one below the first entry, wrapping around at 0).
	search := uint32(int32(b[in]) - '0')
	upper := index[search][0]
	lower := upper
	upper += index[search][1]
	lower--

	step := (upper - lower) / 2
	if step == 0 {
		return 0
	}
	search = lower + step

	firstdig := in
	in1, in2 := false, false
	aux1, aux2 := table[search][0], table[search][1]
	ai := 0
	for {
		if (in1 || b[firstdig] >= aux1[ai]) && (in2 || b[firstdig] <= aux2[ai]) {
			if b[firstdig] > aux1[ai] {
				in1 = true
			}
			if b[firstdig] < aux2[ai] {
				in2 = true
			}
			if in1 && in2 {
				break
			}
			firstdig++
			ai++
			if !(aux1[ai] != 0 && aux2[ai] != 0 && b[firstdig] != 0) {
				break
			}
			if !isDigit(aux1[ai]) {
				ai++
			}
		} else {
			// Check in what direction we should go.
			if b[firstdig] < aux1[ai] && !in1 {
				upper = search
			} else {
				lower = search
			}
			step = (upper - lower) / 2
			search = lower + step

			firstdig = in
			in1, in2 = false, false
			aux1, aux2 = table[search][0], table[search][1]
			ai = 0
		}
		if step == 0 {
			break
		}
	}

	if step != 0 {
		o, i := out, in
		p := table[search][0]
		for pi := 0; p[pi] != 0 && b[i] != 0; pi++ {
			if p[pi] != '-' {
				b[o] = b[i]
				o++
				i++
			} else {
				b[o] = '-'
				o++
			}
			ret++
		}
		b[o] = '-'
		o++
		b[o] = b[i] // add a lookahead char
		return ret + 1
	}
	return ret
}

// weightCheckdig returns the weighted (ISBN/ISSN) check digit of the
// normalized number at b[i:], 0-10 (10 is X).
func weightCheckdig(b []byte, i int, size uint32) uint32 {
	var weight uint32
	for b[i] != 0 && size > 1 {
		if isDigit(b[i]) {
			weight += size * uint32(b[i]-'0')
			size--
		}
		i++
	}
	weight %= 11
	if weight != 0 {
		weight = 11 - weight
	}
	return weight
}

// checkdig returns the EAN13 check digit (0-9) of the number at b[i:].
func checkdig(b []byte, i int, size uint32) uint32 {
	var check, check3, pos uint32
	if b[i] == 'M' { // ISMN start with 'M'
		check3 = 3
		pos = 1
	}
	for b[i] != 0 && size > 1 {
		if isDigit(b[i]) {
			if pos%2 != 0 {
				check3 += uint32(b[i] - '0')
			} else {
				check += uint32(b[i] - '0')
			}
			pos++
			size--
		}
		i++
	}
	check = (check + 3*check3) % 10
	if check != 0 {
		check = 10 - check
	}
	return check
}

func errInvalidText(format string, args ...any) error {
	return pg.Errorf(pg.CodeInvalidTextRepresentation, format, args...)
}

func errOutOfRange(format string, args ...any) error {
	return pg.Errorf(pg.CodeNumericValueOutOfRange, format, args...)
}

// ean2isn checks that an ean13 can be cast to the accept type.
func ean2isn(ean uint64, accept isnType) (uint64, error) {
	typ := typeInvalid
	var buf [maxEAN13Len + 1]byte
	ret := ean

	ean >>= 1
	if ean > 9999999999999 {
		return 0, errOutOfRange("value \"%d\" is out of range for %s type", ean, isnNames[typ])
	}

	// Convert the number.
	search := uint32(0)
	aux := 13
	buf[aux] = 0
	for {
		digval := ean % 10
		ean /= 10
		aux--
		buf[aux] = byte(digval) + '0'
		if ean == 0 {
			break
		}
		cond := search < 12
		search++
		if !cond {
			break
		}
	}
	for {
		cond := search < 12
		search++
		if !cond {
			break
		}
		aux--
		buf[aux] = '0'
	}

	// Find out the data type.
	switch {
	case strncmpEq("978", buf[:], 0, 3):
		typ = typeISBN
	case strncmpEq("977", buf[:], 0, 3):
		typ = typeISSN
	case strncmpEq("9790", buf[:], 0, 4):
		typ = typeISMN
	case strncmpEq("979", buf[:], 0, 3):
		typ = typeISBN
	case buf[0] == '0':
		typ = typeUPC
	default:
		typ = typeEAN13
	}
	if accept != typeAny && accept != typeEAN13 && accept != typ {
		if typ != typeEAN13 {
			return 0, errInvalidText("cannot cast EAN13(%s) to %s for number: \"%s\"",
				isnNames[typ], isnNames[accept], cString(buf[:], 0))
		}
		return 0, errInvalidText("cannot cast %s to %s for number: \"%s\"",
			isnNames[typ], isnNames[accept], cString(buf[:], 0))
	}
	return ret, nil
}

// ean2ISBN and friends convert a normalized EAN13 string, in place, to the
// short UPC/ISxN form.
func ean2ISBN(b []byte) {
	// 978-0-000-00000-0, or ISBN-13 979-..., which has no short form.
	if strncmpEq("978-", b, 0, 4) {
		hyphenate(b, 0, 4, nil, nil)
		check := weightCheckdig(b, 0, 10)
		aux := 0
		for b[aux] != 0 {
			aux++
		}
		for {
			aux--
			if isDigit(b[aux]) {
				break
			}
		}
		if check == 10 {
			b[aux] = 'X'
		} else {
			b[aux] = byte(check) + '0'
		}
	}
}

func ean2ISMN(b []byte) {
	// 979-0-000-00000-0: strip the first part and change the '0' to 'M'.
	hyphenate(b, 0, 4, nil, nil)
	b[0] = 'M'
}

func ean2ISSN(b []byte) {
	// 977-0000-000-00-0: strip, crop, and calculate the new check digit.
	hyphenate(b, 0, 4, nil, nil)
	check := weightCheckdig(b, 0, 8)
	if check == 10 {
		b[8] = 'X'
	} else {
		b[8] = byte(check) + '0'
	}
	b[9] = 0
}

func ean2UPC(b []byte) {
	// 000-000000000-0: strip the first part, crop, and dehyphenate.
	dehyphenate(b, 0, 1)
	b[12] = 0
}

// str2ean converts a string of digits into an ean13 (with room for the flag).
func str2ean(b []byte, i int) uint64 {
	var ean uint64
	for b[i] != 0 {
		if isDigit(b[i]) {
			ean = 10*ean + uint64(b[i]-'0')
		}
		i++
	}
	return ean << 1
}

// ean2string converts an ean13 to its hyphenated string (shortType: in the
// old ISxN short format when possible).
func ean2string(ean uint64, shortType bool) (string, error) {
	var result [maxEAN13Len + 1]byte
	b := result[:]
	typ := typeInvalid
	var valid byte
	if ean&1 != 0 {
		valid = '!'
	}
	ean >>= 1
	if ean > 9999999999999 {
		return "", errOutOfRange("value \"%d\" is out of range for %s type", ean, isnNames[typ])
	}

	// Convert the number.
	search := uint32(0)
	aux := maxEAN13Len
	b[aux] = 0
	aux--
	b[aux] = valid // '!' for numbers with an invalid but corrected check digit
	for {
		digval := ean % 10
		ean /= 10
		aux--
		b[aux] = byte(digval) + '0'
		if search == 0 {
			aux--
			b[aux] = '-' // the check digit is always there
		}
		if ean == 0 {
			break
		}
		cond := search < 13
		search++
		if !cond {
			break
		}
	}
	for {
		cond := search < 13
		search++
		if !cond {
			break
		}
		aux--
		b[aux] = '0'
	}

	// The string should be in this form: ???DDDDDDDDDDDD-D"
	search = hyphenate(b, 0, 3, EAN13_range, &EAN13_index)
	if search != 0 {
		var table [][2][]byte
		var index *[10][2]uint32
		switch {
		case strncmpEq("978-", b, 0, search):
			typ, table, index = typeISBN, ISBN_range, &ISBN_index
		case strncmpEq("977-", b, 0, search):
			typ, table, index = typeISSN, ISSN_range, &ISSN_index
		case strncmpEq("979-0", b, 0, search+1):
			typ, table, index = typeISMN, ISMN_range, &ISMN_index
		case strncmpEq("979-", b, 0, search):
			typ, table, index = typeISBN, ISBN_range_new, &ISBN_index_new
		case b[0] == '0':
			typ, table, index = typeUPC, UPC_range, &UPC_index
		default:
			typ = typeEAN13
		}
		// Verify it's a logically valid EAN13/UPC/ISxN.
		digval := int(search)
		if hyphenate(b, digval, digval+2, table, index) == 0 {
			hyphenate(b, digval, digval+2, nil, nil)
		}
	} else {
		hyphenate(b, 0, 3, nil, nil)
	}

	if shortType {
		switch typ {
		case typeISBN:
			ean2ISBN(b)
		case typeISMN:
			ean2ISMN(b)
		case typeISSN:
			ean2ISSN(b)
		case typeUPC:
			ean2UPC(b)
		}
	}
	return cString(b, 0), nil
}

// string2ean parses a string into an ean13 of the accept type. If the
// string ends with '!' it's always treated as having an invalid check digit.
func string2ean(str string, accept isnType) (uint64, error) {
	s := append([]byte(str), 0, 0)
	buf := []byte("                \x00") // 16 spaces
	aux1 := 3                             // leave space for the first part
	a2 := 0
	typ := typeInvalid
	var check uint32
	rcheck := ^uint32(0)
	length := 0
	magic, valid := false, true

	invalid := func() (uint64, error) {
		return 0, errInvalidText("invalid input syntax for %s number: \"%s\"", isnNames[accept], str)
	}
	wrongType := func() (uint64, error) {
		return 0, errInvalidText("cannot cast %s to %s for number: \"%s\"", isnNames[typ], isnNames[accept], str)
	}

	// Recognize and validate the number.
	for s[a2] != 0 && length <= 13 {
		c := s[a2]
		last := s[a2+1] == '!' || s[a2+1] == 0
		digit := isDigit(c)
		if c == '?' && last { // automagically calculate check digit
			magic, digit = true, true
		}
		switch {
		case length == 0 && (c == 'M' || c == 'm'):
			// only ISMN can be here
			if typ != typeInvalid {
				return invalid()
			}
			typ = typeISMN
			buf[aux1] = 'M'
			aux1++
			length++
		case length == 7 && (digit || c == 'X' || c == 'x') && last:
			// only ISSN can be here
			if typ != typeInvalid {
				return invalid()
			}
			typ = typeISSN
			buf[aux1] = asciiUpper(c)
			aux1++
			length++
		case length == 9 && (digit || c == 'X' || c == 'x') && last:
			// only ISBN and ISMN can be here
			if typ != typeInvalid && typ != typeISMN {
				return invalid()
			}
			if typ == typeInvalid {
				typ = typeISBN // ISMN must start with 'M'
			}
			buf[aux1] = asciiUpper(c)
			aux1++
			length++
		case length == 11 && digit && last:
			// only UPC can be here
			if typ != typeInvalid {
				return invalid()
			}
			typ = typeUPC
			buf[aux1] = c
			aux1++
			length++
		case c == '-' || c == ' ':
			// skip
		case c == '!' && s[a2+1] == 0:
			// the invalid check digit suffix
			if !magic {
				valid = false
			}
			magic = true
		case !digit:
			return invalid()
		default:
			buf[aux1] = c
			aux1++
			length++
			if length > 13 {
				return 0, errOutOfRange("value \"%s\" is out of range for %s type", str, isnNames[accept])
			}
		}
		a2++
	}
	buf[aux1] = 0

	// Find the current check digit value.
	digitAt := func(i int) uint32 { return uint32(int32(buf[i]) - '0') }
	switch length {
	case 13:
		// only EAN13 can be here
		if typ != typeInvalid {
			return invalid()
		}
		typ = typeEAN13
		check = digitAt(15)
	case 12:
		// only UPC can be here
		if typ != typeUPC {
			return invalid()
		}
		check = digitAt(14)
	case 10:
		if typ != typeISBN && typ != typeISMN {
			return invalid()
		}
		if buf[12] == 'X' {
			check = 10
		} else {
			check = digitAt(12)
		}
	case 8:
		if typ != typeInvalid && typ != typeISSN {
			return invalid()
		}
		typ = typeISSN
		if buf[10] == 'X' {
			check = 10
		} else {
			check = digitAt(10)
		}
	default:
		return invalid()
	}
	if typ == typeInvalid {
		return invalid()
	}

	// Obtain the real check digit value, validate, and convert to ean13.
	if accept == typeEAN13 && typ != accept {
		return wrongType()
	}
	if accept != typeAny && typ != typeEAN13 && typ != accept {
		return wrongType()
	}
	switch typ {
	case typeEAN13:
		if valid {
			rcheck = checkdig(buf, 3, 13)
			valid = rcheck == check || magic
		}
		// now get the subtype of EAN13
		switch {
		case buf[3] == '0':
			typ = typeUPC
		case strncmpEq("977", buf, 3, 3):
			typ = typeISSN
		case strncmpEq("978", buf, 3, 3):
			typ = typeISBN
		case strncmpEq("9790", buf, 3, 4):
			typ = typeISMN
		case strncmpEq("979", buf, 3, 3):
			typ = typeISBN
		}
		if accept != typeEAN13 && accept != typeAny && typ != accept {
			return wrongType()
		}
	case typeISMN:
		copy(buf, "9790") // for now ISMN is only 9790
		if valid {
			rcheck = checkdig(buf, 0, 13)
			valid = rcheck == check || magic
		}
	case typeISBN:
		copy(buf, "978")
		if valid {
			rcheck = weightCheckdig(buf, 3, 10)
			valid = rcheck == check || magic
		}
	case typeISSN:
		copy(buf[10:], "00") // the normal issue publication code
		copy(buf, "977")
		if valid {
			rcheck = weightCheckdig(buf, 3, 8)
			valid = rcheck == check || magic
		}
	case typeUPC:
		buf[2] = '0'
		if valid {
			rcheck = checkdig(buf, 2, 13)
			valid = rcheck == check || magic
		}
	}

	// Fix the check digit.
	a := 0
	for buf[a] != 0 && buf[a] <= ' ' {
		a++
	}
	buf[a+12] = byte(checkdig(buf, a, 13)) + '0'
	buf[a+13] = 0

	if !valid && !magic {
		if weak.Get() {
			// Weak input mode: set the "invalid-check-digit-on-input" flag.
			return str2ean(buf, a) | 1, nil
		}
		if rcheck == ^uint32(0) {
			return 0, errInvalidText("invalid %s number: \"%s\"", isnNames[accept], str)
		}
		should := byte(rcheck) + '0'
		if rcheck == 10 {
			should = 'X'
		}
		return 0, errInvalidText("invalid check digit for %s number: \"%s\", should be %c",
			isnNames[accept], str, should)
	}
	result := str2ean(buf, a)
	if !valid {
		result |= 1
	}
	return result, nil
}

// ---- SQL-callable functions (declared by sql/isn--*.sql) ----------------

//gopgext:function symbol=isn_out manual
func IsnOut(v int64) (pg.CString, error) {
	s, err := ean2string(uint64(v), true)
	return pg.CString(s), err
}

//gopgext:function symbol=ean13_out manual
func Ean13Out(v int64) (pg.CString, error) {
	s, err := ean2string(uint64(v), false)
	return pg.CString(s), err
}

func in(s pg.CString, accept isnType) (int64, error) {
	v, err := string2ean(string(s), accept)
	return int64(v), err
}

//gopgext:function symbol=ean13_in manual
func Ean13In(s pg.CString) (int64, error) { return in(s, typeEAN13) }

//gopgext:function symbol=isbn_in manual
func IsbnIn(s pg.CString) (int64, error) { return in(s, typeISBN) }

//gopgext:function symbol=ismn_in manual
func IsmnIn(s pg.CString) (int64, error) { return in(s, typeISMN) }

//gopgext:function symbol=issn_in manual
func IssnIn(s pg.CString) (int64, error) { return in(s, typeISSN) }

//gopgext:function symbol=upc_in manual
func UpcIn(s pg.CString) (int64, error) { return in(s, typeUPC) }

func castFromEAN13(v int64, accept isnType) (int64, error) {
	r, err := ean2isn(uint64(v), accept)
	return int64(r), err
}

//gopgext:function symbol=isbn_cast_from_ean13 manual
func IsbnCastFromEan13(v int64) (int64, error) { return castFromEAN13(v, typeISBN) }

//gopgext:function symbol=ismn_cast_from_ean13 manual
func IsmnCastFromEan13(v int64) (int64, error) { return castFromEAN13(v, typeISMN) }

//gopgext:function symbol=issn_cast_from_ean13 manual
func IssnCastFromEan13(v int64) (int64, error) { return castFromEAN13(v, typeISSN) }

//gopgext:function symbol=upc_cast_from_ean13 manual
func UpcCastFromEan13(v int64) (int64, error) { return castFromEAN13(v, typeUPC) }

// IsValid returns false if the "invalid-check-digit-on-input" flag is set.
//
//gopgext:function symbol=is_valid manual
func IsValid(v int64) bool { return v&1 == 0 }

// MakeValid clears the "invalid-check-digit-on-input" flag.
//
//gopgext:function symbol=make_valid manual
func MakeValid(v int64) int64 { return v &^ 1 }

// AcceptWeakInput sets isn.weak (for the session) and returns it.
//
//gopgext:function symbol=accept_weak_input manual
func AcceptWeakInput(on bool) bool {
	value := "off"
	if on {
		value = "on"
	}
	pg.SetConfig("isn.weak", value, false)
	return weak.Get()
}

//gopgext:function symbol=weak_input_status manual
func WeakInputStatus() bool { return weak.Get() }
