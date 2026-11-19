package main

// Daitch-Mokotoff Soundex, ported from contrib/fuzzystrmatch/daitch_mokotoff.c
// (Dag Lem). The coding chart is in dm_table.go, generated from the
// original's daitch_mokotoff_header.pl.

// DaitchMokotoff returns the Daitch-Mokotoff soundex codes of s, NULL if it
// has no encodable characters.
//
//gopgext:function immutable parallel=safe
func DaitchMokotoff(s string) *[]string {
	var codes []string
	if !daitchMokotoffCoding(s, &codes) {
		return nil
	}
	return &codes
}

type dmCodes [3]string // start of name, before a vowel, any other

type dmLetter struct {
	letter  byte
	letters []dmLetter // possible successive letters
	codes   *[2]dmCodes
}

const dmCodeDigits = 6

// dmNode is a node in the soundex code tree.
type dmNode struct {
	soundexLength  int
	soundex        [dmCodeDigits]byte
	isLeaf         bool
	lastUpdate     int
	codeDigit      byte
	prevCodeDigits [2]byte
	nextCodeDigits [2]byte
	prevCodeIndex  int
	nextCodeIndex  int
	children       [10]*dmNode
	next           [2]*dmNode
}

func newDMNode() *dmNode {
	return &dmNode{soundex: [dmCodeDigits]byte{'0', '0', '0', '0', '0', '0'}}
}

// Dummy soundex codes at end of input.
var endCodes = [2]dmCodes{{"X", "X", "X"}}

// Mapping from ISO8859-1 to upper-case ASCII, covering the range 0x60..0xFF.
const iso8859ToASCIIUpper = "`ABCDEFGHIJKLMNOPQRSTUVWXYZ{|}~                                  !                             ?AAAAAAECEEEEIIIIDNOOOOO*OUUUUYDSAAAAAAECEEEEIIIIDNOOOOO/OUUUUYDY"

type dmState struct {
	firstNode [2]*dmNode
	lastNode  [2]*dmNode
	out       *[]string
}

func (n *dmNode) initialize(lastUpdate int) {
	if n.lastUpdate < lastUpdate {
		n.prevCodeDigits = n.nextCodeDigits
		n.nextCodeDigits = [2]byte{}
		n.prevCodeIndex = n.nextCodeIndex
		n.nextCodeIndex = 0
		n.isLeaf = false
		n.lastUpdate = lastUpdate
	}
}

func (n *dmNode) addNextCodeDigit(codeIndex int, digit byte) {
	n.nextCodeIndex |= codeIndex
	if n.nextCodeDigits[0] == 0 {
		n.nextCodeDigits[0] = digit
	} else if n.nextCodeDigits[0] != digit {
		n.nextCodeDigits[1] = digit
	}
}

func (st *dmState) setLeaf(node *dmNode, ix int) {
	if !node.isLeaf {
		node.isLeaf = true
		if st.firstNode[ix] == nil {
			st.firstNode[ix] = node
		} else {
			st.lastNode[ix].next[ix] = node
		}
		st.lastNode[ix] = node
		node.next[ix] = nil
	}
}

func (st *dmState) findOrCreateChild(parent *dmNode, digit byte) *dmNode {
	i := digit - '0'
	if node := parent.children[i]; node != nil {
		// Found existing child node. Skip completed nodes.
		if node.soundexLength < dmCodeDigits {
			return node
		}
		return nil
	}
	node := newDMNode()
	parent.children[i] = node
	node.soundex = parent.soundex
	node.soundexLength = parent.soundexLength
	node.soundex[node.soundexLength] = digit
	node.soundexLength++
	node.codeDigit = digit
	node.nextCodeIndex = node.prevCodeIndex
	if node.soundexLength < dmCodeDigits {
		return node
	}
	// Completed soundex code.
	*st.out = append(*st.out, string(node.soundex[:]))
	return nil
}

func (st *dmState) updateNode(node *dmNode, ix, letterNo, prevCodeIndex, nextCodeIndex int,
	nextCodeDigits string, digitNo int) {
	digit := nextCodeDigits[digitNo]
	var dirty []*dmNode

	node.initialize(letterNo)
	if node.prevCodeIndex != 0 && node.prevCodeIndex&prevCodeIndex == 0 {
		// The sound (vowel / consonant) of this letter encoding doesn't
		// correspond to the coding index of the previous letter.
		return
	}
	if digit == 'X' || (digitNo == 0 && (node.prevCodeDigits[0] == digit || node.prevCodeDigits[1] == digit)) {
		// The code digit is the same as one of the previous (not added).
		dirty = append(dirty, node)
	}
	if digit != 'X' && (digitNo > 0 || node.prevCodeDigits[0] != digit || node.prevCodeDigits[1] != 0) {
		// The code digit is different from one of the previous (added).
		if child := st.findOrCreateChild(node, digit); child != nil {
			child.initialize(letterNo)
			dirty = append(dirty, child)
		}
	}
	for _, d := range dirty {
		d.addNextCodeDigit(nextCodeIndex, digit)
		// Like the original (next_code_digits[++digit_no]), digitNo
		// advances once per dirty node.
		digitNo++
		if digitNo < len(nextCodeDigits) {
			st.updateNode(d, ix, letterNo, prevCodeIndex, nextCodeIndex, nextCodeDigits, digitNo)
		} else {
			st.setLeaf(d, ix)
		}
	}
}

func (st *dmState) updateLeaves(ix *int, letterNo int, codes, nextCodes *[2]dmCodes) {
	ixNext := (*ix + 1) & 1
	st.firstNode[ixNext] = nil
	st.lastNode[ixNext] = nil
	for node := st.firstNode[*ix]; node != nil; node = node.next[*ix] {
		for i := 0; i < 2 && codes[i][0] != ""; i++ {
			code := codes[i]
			// Coding for previous letter - before vowel: 1, all other: 2.
			prevCodeIndex := 1
			if code[0][0] > '1' {
				prevCodeIndex = 2
			}
			for j := 0; j < 2 && nextCodes[j][0] != ""; j++ {
				var codeIndex int
				switch {
				case letterNo == 0:
					codeIndex = 0 // first letter
				case nextCodes[j][0][0] <= '1':
					codeIndex = 1 // the next letter is a vowel
				default:
					codeIndex = 2
				}
				st.updateNode(node, ixNext, letterNo, prevCodeIndex, codeIndex, code[codeIndex], 0)
			}
		}
	}
	*ix = ixNext
}

// utf8ToUnicode decodes the character at s[0] like PostgreSQL's
// utf8_to_unicode (s is NUL-padded).
func utf8ToUnicode(s []byte) uint32 {
	c := s[0]
	switch {
	case c&0x80 == 0:
		return uint32(c)
	case c&0xe0 == 0xc0:
		return uint32(c&0x1f)<<6 | uint32(s[1]&0x3f)
	case c&0xf0 == 0xe0:
		return uint32(c&0x0f)<<12 | uint32(s[1]&0x3f)<<6 | uint32(s[2]&0x3f)
	case c&0xf8 == 0xf0:
		return uint32(c&0x07)<<18 | uint32(s[1]&0x3f)<<12 | uint32(s[2]&0x3f)<<6 | uint32(s[3]&0x3f)
	}
	return 0xffffffff
}

// readChar returns the next character, converted to uppercase ASCII.
func readChar(str []byte, ix *int) byte {
	const na = '\x1a' // substitute for skipped code points
	c := utf8ToUnicode(str[*ix:])
	if c != 0 {
		*ix += utf8Len(str[*ix])
	}
	switch {
	case c >= '[' && c <= ']':
		return na // reserved for the conversions below
	case c < 0x60:
		return byte(c)
	case c < 0x100:
		return iso8859ToASCIIUpper[c-0x60]
	}
	switch c {
	case 0x0104, 0x0105: // A with ogonek
		return '['
	case 0x0118, 0x0119: // E with ogonek
		return '\\'
	case 0x0162, 0x0163, 0x021A, 0x021B: // T with cedilla / comma below
		return ']'
	}
	return na
}

// readValidChar reads the next character in [A-\]], or NUL at the end.
func readValidChar(str []byte, ix *int) byte {
	for {
		c := readChar(str, ix)
		if c == 0 || (c >= 'A' && c <= ']') {
			return c
		}
	}
}

// readLetter returns the sound coding for the next letter (sequence).
func readLetter(str []byte, ix *int) *[2]dmCodes {
	c := readValidChar(str, ix)
	if c == 0 {
		return nil
	}
	letter := &letter_[c-'A']
	codes := letter.codes
	i := *ix
	for letter.letters != nil {
		c = readValidChar(str, &i)
		if c == 0 {
			break
		}
		found := false
		for j := range letter.letters {
			if letter.letters[j].letter == c {
				letter = &letter.letters[j]
				if letter.codes != nil {
					codes = letter.codes
					*ix = i
				}
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	return codes
}

// daitchMokotoffCoding appends all codes for word to out. It returns false
// if word has no encodable characters.
func daitchMokotoffCoding(word string, out *[]string) bool {
	str := append([]byte(word), 0, 0, 0, 0)
	i, letterNo, ix := 0, 0, 0
	codes := readLetter(str, &i)
	if codes == nil {
		return false
	}
	st := &dmState{out: out}
	st.firstNode[ix] = newDMNode()
	for codes != nil && st.firstNode[ix] != nil {
		nextCodes := readLetter(str, &i)
		nc := nextCodes
		if nc == nil {
			nc = &endCodes
		}
		st.updateLeaves(&ix, letterNo, codes, nc)
		codes = nextCodes
		letterNo++
	}
	// Append all remaining (incomplete) codes.
	for node := st.firstNode[ix]; node != nil; node = node.next[ix] {
		*out = append(*out, string(node.soundex[:]))
	}
	return true
}
