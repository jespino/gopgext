package main

// Double Metaphone, ported from contrib/fuzzystrmatch/dmetaphone.c (itself
// from the Text::DoubleMetaphone Perl module by Maurice Aubrey, after
// Lawrence Philips' algorithm).

import (
	"strings"
	"unsafe"
)

//gopgext:function immutable parallel=safe
func Dmetaphone(s string) string {
	primary, _ := doubleMetaphone(s)
	return primary
}

//gopgext:function immutable parallel=safe
func DmetaphoneAlt(s string) string {
	_, secondary := doubleMetaphone(s)
	return secondary
}

//export fuzzystrmatch_toupper
func cToUpper(s *byte, n int64, out **byte) uintptr

// toUpper upper-cases s with the database's default collation
// (str_toupper). If PostgreSQL raises an error, it panics: gopgext re-raises
// the original error when the function returns.
func toUpper(s string) string {
	b := []byte(s + "\x00")
	var out *byte
	if cToUpper(&b[0], int64(len(s)), &out) != 0 {
		panic("str_toupper failed")
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(out), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(out, n))
}

// metaString is the original's view of the upper-cased, space-padded input:
// out-of-range reads return NUL, and comparisons stop at the end.
type metaString string

func (s metaString) at(pos int) byte {
	if pos < 0 || pos >= len(s) {
		return 0
	}
	return s[pos]
}

func (s metaString) isVowel(pos int) bool {
	switch s.at(pos) {
	case 'A', 'E', 'I', 'O', 'U', 'Y':
		return true
	}
	return false
}

// stringAt reports whether one of tests occurs at start (strncmp of length
// bytes, like the original).
func (s metaString) stringAt(start, length int, tests ...string) bool {
	if start < 0 || start >= len(s) {
		return false
	}
	for _, t := range tests {
		if strncmpEqual(string(s[start:]), t, length) {
			return true
		}
	}
	return false
}

func strncmpEqual(a, b string, n int) bool {
	for i := 0; i < n; i++ {
		var ca, cb byte
		if i < len(a) {
			ca = a[i]
		}
		if i < len(b) {
			cb = b[i]
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

func (s metaString) slavoGermanic() bool {
	str := string(s)
	return strings.Contains(str, "W") || strings.Contains(str, "K") ||
		strings.Contains(str, "CZ") || strings.Contains(str, "WITZ")
}

func doubleMetaphone(str string) (string, string) {
	length := len(str)
	last := length - 1
	original := metaString(toUpper(str + "     "))
	var primary, secondary strings.Builder
	add := func(p, s string) {
		primary.WriteString(p)
		secondary.WriteString(s)
	}
	current := 0

	// Skip these when at start of word.
	if original.stringAt(0, 2, "GN", "KN", "PN", "WR", "PS") {
		current++
	}
	// Initial 'X' is pronounced 'Z' e.g. 'Xavier'.
	if original.at(0) == 'X' {
		add("S", "S")
		current++
	}

	for primary.Len() < 4 || secondary.Len() < 4 {
		if current >= length {
			break
		}
		switch original.at(current) {
		case 'A', 'E', 'I', 'O', 'U', 'Y':
			if current == 0 {
				add("A", "A")
			}
			current++

		case 'B':
			add("P", "P")
			if original.at(current+1) == 'B' {
				current += 2
			} else {
				current++
			}

		case '\xc7': // C with cedilla
			add("S", "S")
			current++

		case 'C':
			// Various germanic.
			if current > 1 && !original.isVowel(current-2) &&
				original.stringAt(current-1, 3, "ACH") &&
				original.at(current+2) != 'I' &&
				(original.at(current+2) != 'E' || original.stringAt(current-2, 6, "BACHER", "MACHER")) {
				add("K", "K")
				current += 2
				break
			}
			// Special case 'caesar'.
			if current == 0 && original.stringAt(current, 6, "CAESAR") {
				add("S", "S")
				current += 2
				break
			}
			// Italian 'chianti'.
			if original.stringAt(current, 4, "CHIA") {
				add("K", "K")
				current += 2
				break
			}
			if original.stringAt(current, 2, "CH") {
				// Find 'michael'.
				if current > 0 && original.stringAt(current, 4, "CHAE") {
					add("K", "X")
					current += 2
					break
				}
				// Greek roots e.g. 'chemistry', 'chorus'.
				if current == 0 &&
					(original.stringAt(current+1, 5, "HARAC", "HARIS") ||
						original.stringAt(current+1, 3, "HOR", "HYM", "HIA", "HEM")) &&
					!original.stringAt(0, 5, "CHORE") {
					add("K", "K")
					current += 2
					break
				}
				// Germanic, greek, or otherwise 'ch' for 'kh' sound.
				if original.stringAt(0, 4, "VAN ", "VON ") || original.stringAt(0, 3, "SCH") ||
					// 'architect but not 'arch', 'orchestra', 'orchid'
					original.stringAt(current-2, 6, "ORCHES", "ARCHIT", "ORCHID") ||
					original.stringAt(current+2, 1, "T", "S") ||
					((original.stringAt(current-1, 1, "A", "O", "U", "E") || current == 0) &&
						// e.g., 'wachtler', 'wechsler', but not 'tichner'
						original.stringAt(current+2, 1, "L", "R", "N", "M", "B", "H", "F", "V", "W", " ")) {
					add("K", "K")
				} else if current > 0 {
					if original.stringAt(0, 2, "MC") {
						add("K", "K") // e.g., "McHugh"
					} else {
						add("X", "K")
					}
				} else {
					add("X", "X")
				}
				current += 2
				break
			}
			// e.g, 'czerny'.
			if original.stringAt(current, 2, "CZ") && !original.stringAt(current-2, 4, "WICZ") {
				add("S", "X")
				current += 2
				break
			}
			// e.g., 'focaccia'.
			if original.stringAt(current+1, 3, "CIA") {
				add("X", "X")
				current += 3
				break
			}
			// Double 'C', but not if e.g. 'McClellan'.
			if original.stringAt(current, 2, "CC") && !(current == 1 && original.at(0) == 'M') {
				// 'bellocchio' but not 'bacchus'
				if original.stringAt(current+2, 1, "I", "E", "H") && !original.stringAt(current+2, 2, "HU") {
					// 'accident', 'accede' 'succeed'
					if (current == 1 && original.at(current-1) == 'A') ||
						original.stringAt(current-1, 5, "UCCEE", "UCCES") {
						add("KS", "KS")
					} else {
						// 'bacci', 'bertucci', other italian
						add("X", "X")
					}
					current += 3
					break
				}
				// Pierce's rule.
				add("K", "K")
				current += 2
				break
			}
			if original.stringAt(current, 2, "CK", "CG", "CQ") {
				add("K", "K")
				current += 2
				break
			}
			if original.stringAt(current, 2, "CI", "CE", "CY") {
				// Italian vs. english.
				if original.stringAt(current, 3, "CIO", "CIE", "CIA") {
					add("S", "X")
				} else {
					add("S", "S")
				}
				current += 2
				break
			}
			add("K", "K")
			// Name sent in 'mac caffrey', 'mac gregor'.
			if original.stringAt(current+1, 2, " C", " Q", " G") {
				current += 3
			} else if original.stringAt(current+1, 1, "C", "K", "Q") && !original.stringAt(current+1, 2, "CE", "CI") {
				current += 2
			} else {
				current++
			}

		case 'D':
			if original.stringAt(current, 2, "DG") {
				if original.stringAt(current+2, 1, "I", "E", "Y") {
					add("J", "J") // e.g. 'edge'
					current += 3
				} else {
					add("TK", "TK") // e.g. 'edgar'
					current += 2
				}
				break
			}
			if original.stringAt(current, 2, "DT", "DD") {
				add("T", "T")
				current += 2
				break
			}
			add("T", "T")
			current++

		case 'F':
			if original.at(current+1) == 'F' {
				current += 2
			} else {
				current++
			}
			add("F", "F")

		case 'G':
			if original.at(current+1) == 'H' {
				if current > 0 && !original.isVowel(current-1) {
					add("K", "K")
					current += 2
					break
				}
				if current < 3 && current == 0 {
					// 'ghislane', ghiradelli
					if original.at(current+2) == 'I' {
						add("J", "J")
					} else {
						add("K", "K")
					}
					current += 2
					break
				}
				// Parker's rule (with some further refinements) - e.g., 'hugh'.
				if (current > 1 && original.stringAt(current-2, 1, "B", "H", "D")) ||
					// e.g., 'bough'
					(current > 2 && original.stringAt(current-3, 1, "B", "H", "D")) ||
					// e.g., 'broughton'
					(current > 3 && original.stringAt(current-4, 1, "B", "H")) {
					current += 2
					break
				}
				// e.g., 'laugh', 'McLaughlin', 'cough', 'gough', 'rough', 'tough'
				if current > 2 && original.at(current-1) == 'U' &&
					original.stringAt(current-3, 1, "C", "G", "L", "R", "T") {
					add("F", "F")
				} else if current > 0 && original.at(current-1) != 'I' {
					add("K", "K")
				}
				current += 2
				break
			}
			if original.at(current+1) == 'N' {
				if current == 1 && original.isVowel(0) && !original.slavoGermanic() {
					add("KN", "N")
				} else if !original.stringAt(current+2, 2, "EY") && original.at(current+1) != 'Y' &&
					!original.slavoGermanic() {
					// not e.g. 'cagney'
					add("N", "KN")
				} else {
					add("KN", "KN")
				}
				current += 2
				break
			}
			// 'tagliaro'
			if original.stringAt(current+1, 2, "LI") && !original.slavoGermanic() {
				add("KL", "L")
				current += 2
				break
			}
			// -ges-, -gep-, -gel-, -gie- at beginning
			if current == 0 && (original.at(current+1) == 'Y' ||
				original.stringAt(current+1, 2, "ES", "EP", "EB", "EL", "EY", "IB", "IL", "IN", "IE", "EI", "ER")) {
				add("K", "J")
				current += 2
				break
			}
			// -ger-, -gy-
			if (original.stringAt(current+1, 2, "ER") || original.at(current+1) == 'Y') &&
				!original.stringAt(0, 6, "DANGER", "RANGER", "MANGER") &&
				!original.stringAt(current-1, 1, "E", "I") &&
				!original.stringAt(current-1, 3, "RGY", "OGY") {
				add("K", "J")
				current += 2
				break
			}
			// italian e.g, 'biaggi'
			if original.stringAt(current+1, 1, "E", "I", "Y") || original.stringAt(current-1, 4, "AGGI", "OGGI") {
				// obvious germanic
				if original.stringAt(0, 4, "VAN ", "VON ") || original.stringAt(0, 3, "SCH") ||
					original.stringAt(current+1, 2, "ET") {
					add("K", "K")
				} else if original.stringAt(current+1, 4, "IER ") {
					// always soft if french ending
					add("J", "J")
				} else {
					add("J", "K")
				}
				current += 2
				break
			}
			if original.at(current+1) == 'G' {
				current += 2
			} else {
				current++
			}
			add("K", "K")

		case 'H':
			// Only keep if first & before vowel or between 2 vowels.
			if (current == 0 || original.isVowel(current-1)) && original.isVowel(current+1) {
				add("H", "H")
				current += 2
			} else {
				current++ // also takes care of 'HH'
			}

		case 'J':
			// Obvious spanish, 'jose', 'san jacinto'.
			if original.stringAt(current, 4, "JOSE") || original.stringAt(0, 4, "SAN ") {
				if (current == 0 && original.at(current+4) == ' ') || original.stringAt(0, 4, "SAN ") {
					add("H", "H")
				} else {
					add("J", "H")
				}
				current++
				break
			}
			if current == 0 && !original.stringAt(current, 4, "JOSE") {
				add("J", "A") // Yankelovich/Jankelowicz
			} else if original.isVowel(current-1) && !original.slavoGermanic() &&
				(original.at(current+1) == 'A' || original.at(current+1) == 'O') {
				// spanish pron. of e.g. 'bajador'
				add("J", "H")
			} else if current == last {
				add("J", "")
			} else if !original.stringAt(current+1, 1, "L", "T", "K", "S", "N", "M", "B", "Z") &&
				!original.stringAt(current-1, 1, "S", "K", "L") {
				add("J", "J")
			}
			if original.at(current+1) == 'J' { // it could happen!
				current += 2
			} else {
				current++
			}

		case 'K':
			if original.at(current+1) == 'K' {
				current += 2
			} else {
				current++
			}
			add("K", "K")

		case 'L':
			if original.at(current+1) == 'L' {
				// spanish e.g. 'cabrillo', 'gallegos'
				if (current == length-3 && original.stringAt(current-1, 4, "ILLO", "ILLA", "ALLE")) ||
					((original.stringAt(last-1, 2, "AS", "OS") || original.stringAt(last, 1, "A", "O")) &&
						original.stringAt(current-1, 4, "ALLE")) {
					add("L", "")
					current += 2
					break
				}
				current += 2
			} else {
				current++
			}
			add("L", "L")

		case 'M':
			if (original.stringAt(current-1, 3, "UMB") &&
				(current+1 == last || original.stringAt(current+2, 2, "ER"))) ||
				// 'dumb','thumb'
				original.at(current+1) == 'M' {
				current += 2
			} else {
				current++
			}
			add("M", "M")

		case 'N':
			if original.at(current+1) == 'N' {
				current += 2
			} else {
				current++
			}
			add("N", "N")

		case '\xd1': // N with tilde
			current++
			add("N", "N")

		case 'P':
			if original.at(current+1) == 'H' {
				add("F", "F")
				current += 2
				break
			}
			// also account for "campbell", "raspberry"
			if original.stringAt(current+1, 1, "P", "B") {
				current += 2
			} else {
				current++
			}
			add("P", "P")

		case 'Q':
			if original.at(current+1) == 'Q' {
				current += 2
			} else {
				current++
			}
			add("K", "K")

		case 'R':
			// french e.g. 'rogier', but exclude 'hochmeier'
			if current == last && !original.slavoGermanic() &&
				original.stringAt(current-2, 2, "IE") && !original.stringAt(current-4, 2, "ME", "MA") {
				add("", "R")
			} else {
				add("R", "R")
			}
			if original.at(current+1) == 'R' {
				current += 2
			} else {
				current++
			}

		case 'S':
			// special cases 'island', 'isle', 'carlisle', 'carlysle'
			if original.stringAt(current-1, 3, "ISL", "YSL") {
				current++
				break
			}
			// special case 'sugar-'
			if current == 0 && original.stringAt(current, 5, "SUGAR") {
				add("X", "S")
				current++
				break
			}
			if original.stringAt(current, 2, "SH") {
				// germanic
				if original.stringAt(current+1, 4, "HEIM", "HOEK", "HOLM", "HOLZ") {
					add("S", "S")
				} else {
					add("X", "X")
				}
				current += 2
				break
			}
			// italian & armenian
			if original.stringAt(current, 3, "SIO", "SIA") || original.stringAt(current, 4, "SIAN") {
				if !original.slavoGermanic() {
					add("S", "X")
				} else {
					add("S", "S")
				}
				current += 3
				break
			}
			// german & anglicisations, e.g. 'smith' match 'schmidt', 'snider'
			// match 'schneider'; also -sz- in slavic languages
			if (current == 0 && original.stringAt(current+1, 1, "M", "N", "L", "W")) ||
				original.stringAt(current+1, 1, "Z") {
				add("S", "X")
				if original.stringAt(current+1, 1, "Z") {
					current += 2
				} else {
					current++
				}
				break
			}
			if original.stringAt(current, 2, "SC") {
				// Schlesinger's rule
				if original.at(current+2) == 'H' {
					// dutch origin, e.g. 'school', 'schooner'
					if original.stringAt(current+3, 2, "OO", "ER", "EN", "UY", "ED", "EM") {
						// 'schermerhorn', 'schenker'
						if original.stringAt(current+3, 2, "ER", "EN") {
							add("X", "SK")
						} else {
							add("SK", "SK")
						}
					} else if current == 0 && !original.isVowel(3) && original.at(3) != 'W' {
						add("X", "S")
					} else {
						add("X", "X")
					}
					current += 3
					break
				}
				if original.stringAt(current+2, 1, "I", "E", "Y") {
					add("S", "S")
				} else {
					add("SK", "SK")
				}
				current += 3
				break
			}
			// french e.g. 'resnais', 'artois'
			if current == last && original.stringAt(current-2, 2, "AI", "OI") {
				add("", "S")
			} else {
				add("S", "S")
			}
			if original.stringAt(current+1, 1, "S", "Z") {
				current += 2
			} else {
				current++
			}

		case 'T':
			if original.stringAt(current, 4, "TION") {
				add("X", "X")
				current += 3
				break
			}
			if original.stringAt(current, 3, "TIA", "TCH") {
				add("X", "X")
				current += 3
				break
			}
			if original.stringAt(current, 2, "TH") || original.stringAt(current, 3, "TTH") {
				// special case 'thomas', 'thames' or germanic
				if original.stringAt(current+2, 2, "OM", "AM") ||
					original.stringAt(0, 4, "VAN ", "VON ") || original.stringAt(0, 3, "SCH") {
					add("T", "T")
				} else {
					add("0", "T")
				}
				current += 2
				break
			}
			if original.stringAt(current+1, 1, "T", "D") {
				current += 2
			} else {
				current++
			}
			add("T", "T")

		case 'V':
			if original.at(current+1) == 'V' {
				current += 2
			} else {
				current++
			}
			add("F", "F")

		case 'W':
			// can also be in middle of word
			if original.stringAt(current, 2, "WR") {
				add("R", "R")
				current += 2
				break
			}
			if current == 0 && (original.isVowel(current+1) || original.stringAt(current, 2, "WH")) {
				// Wasserman should match Vasserman
				if original.isVowel(current + 1) {
					add("A", "F")
				} else {
					// need Uomo to match Womo
					add("A", "A")
				}
			}
			// Arnow should match Arnoff
			if (current == last && original.isVowel(current-1)) ||
				original.stringAt(current-1, 5, "EWSKI", "EWSKY", "OWSKI", "OWSKY") ||
				original.stringAt(0, 3, "SCH") {
				add("", "F")
				current++
				break
			}
			// polish e.g. 'filipowicz'
			if original.stringAt(current, 4, "WICZ", "WITZ") {
				add("TS", "FX")
				current += 4
				break
			}
			current++ // else skip it

		case 'X':
			// french e.g. breaux
			if !(current == last &&
				(original.stringAt(current-3, 3, "IAU", "EAU") || original.stringAt(current-2, 2, "AU", "OU"))) {
				add("KS", "KS")
			}
			if original.stringAt(current+1, 1, "C", "X") {
				current += 2
			} else {
				current++
			}

		case 'Z':
			// chinese pinyin e.g. 'zhao'
			if original.at(current+1) == 'H' {
				add("J", "J")
				current += 2
				break
			} else if original.stringAt(current+1, 2, "ZO", "ZI", "ZA") ||
				(original.slavoGermanic() && current > 0 && original.at(current-1) != 'T') {
				add("S", "TS")
			} else {
				add("S", "S")
			}
			if original.at(current+1) == 'Z' {
				current += 2
			} else {
				current++
			}

		default:
			current++
		}
	}
	p, s := primary.String(), secondary.String()
	if len(p) > 4 {
		p = p[:4]
	}
	if len(s) > 4 {
		s = s[:4]
	}
	return p, s
}
