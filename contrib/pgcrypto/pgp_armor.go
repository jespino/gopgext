package main

// ASCII armor (pgp-armor.c): base64 with a CRC24 checksum between
// "-----BEGIN PGP MESSAGE-----" and "-----END PGP MESSAGE-----" lines.

import (
	"bytes"
	"strings"
)

const armorBase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// armorBase64Encode is pg_base64_encode: lines of 76 characters.
func armorBase64Encode(dst, src []byte) []byte {
	lineStart := len(dst)
	for i := 0; i < len(src); i += 3 {
		var buf uint32
		n := min(3, len(src)-i)
		for j := 0; j < n; j++ {
			buf |= uint32(src[i+j]) << (16 - 8*j)
		}
		if n < 3 {
			dst = append(dst, armorBase64[buf>>18&0x3f], armorBase64[buf>>12&0x3f])
			if n == 2 {
				dst = append(dst, armorBase64[buf>>6&0x3f])
			} else {
				dst = append(dst, '=')
			}
			dst = append(dst, '=')
			break
		}
		dst = append(dst, armorBase64[buf>>18&0x3f], armorBase64[buf>>12&0x3f],
			armorBase64[buf>>6&0x3f], armorBase64[buf&0x3f])
		if len(dst)-lineStart >= 76 {
			dst = append(dst, '\n')
			lineStart = len(dst)
		}
	}
	return dst
}

// armorBase64Decode is pg_base64_decode (whitespace is skipped).
func armorBase64Decode(src []byte) ([]byte, error) {
	var dst []byte
	var buf uint32
	pos, end := 0, 0
	for _, c := range src {
		var b uint32
		switch {
		case c >= 'A' && c <= 'Z':
			b = uint32(c - 'A')
		case c >= 'a' && c <= 'z':
			b = uint32(c-'a') + 26
		case c >= '0' && c <= '9':
			b = uint32(c-'0') + 52
		case c == '+':
			b = 62
		case c == '/':
			b = 63
		case c == '=':
			if end == 0 {
				switch pos {
				case 2:
					end = 1
				case 3:
					end = 2
				default:
					return nil, pxePgpCorruptArmor
				}
			}
			b = 0
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		default:
			return nil, pxePgpCorruptArmor
		}
		buf = buf<<6 + b
		pos++
		if pos == 4 {
			dst = append(dst, byte(buf>>16))
			if end == 0 || end > 1 {
				dst = append(dst, byte(buf>>8))
			}
			if end == 0 || end > 2 {
				dst = append(dst, byte(buf))
			}
			buf, pos = 0, 0
		}
	}
	if pos != 0 {
		return nil, pxePgpCorruptArmor
	}
	return dst, nil
}

// armorCRC24 is crc24 (RFC 2440).
func armorCRC24(data []byte) uint32 {
	crc := uint32(0xb704ce)
	for _, b := range data {
		crc ^= uint32(b) << 16
		for i := 0; i < 8; i++ {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864cfb
			}
		}
	}
	return crc & 0xffffff
}

// armorEncode is pgp_armor_encode.
func armorEncode(src []byte, keys, values []string) string {
	dst := []byte("-----BEGIN PGP MESSAGE-----\n")
	for i := range keys {
		dst = append(dst, keys[i]+": "+values[i]+"\n"...)
	}
	dst = append(dst, '\n')
	dst = armorBase64Encode(dst, src)
	if dst[len(dst)-1] != '\n' {
		dst = append(dst, '\n')
	}
	crc := armorCRC24(src)
	dst = append(dst, '=', armorBase64[crc>>18&0x3f], armorBase64[crc>>12&0x3f],
		armorBase64[crc>>6&0x3f], armorBase64[crc&0x3f])
	dst = append(dst, "\n-----END PGP MESSAGE-----\n"...)
	return string(dst)
}

// armorFindHeader is find_header: the start of the first "-----BEGIN" (or
// "-----END") line at or after from, and the length of that line.
func armorFindHeader(data []byte, from int, isEnd bool) (int, int, error) {
	sep := "-----BEGIN"
	if isEnd {
		sep = "-----END"
	}
	p := from
	for {
		i := bytes.Index(data[p:], []byte(sep))
		if i < 0 {
			return 0, 0, pxePgpCorruptArmor
		}
		p += i
		if p == from || data[p-1] == '\n' {
			break
		}
		p += len(sep)
	}
	start := p
	p += len(sep)
	for ; p < len(data) && data[p] != '-'; p++ {
		if data[p] < ' ' {
			return 0, 0, pxePgpCorruptArmor
		}
	}
	if len(data)-p < 5 || string(data[p:p+5]) != sep[:5] {
		return 0, 0, pxePgpCorruptArmor
	}
	p += 5
	if p < len(data) {
		if data[p] != '\n' && data[p] != '\r' {
			return 0, 0, pxePgpCorruptArmor
		}
		if data[p] == '\r' {
			p++
		}
		if p < len(data) && data[p] == '\n' {
			p++
		}
	}
	return start, p - start, nil
}

// armorBody finds the armor lines: the end of the BEGIN line, the start of
// the base64 data (after the headers and the empty line), and the start of
// the END line.
func armorBody(data []byte) (hdrStart, b64Start, armorEnd int, err error) {
	start, n, err := armorFindHeader(data, 0, false)
	if err != nil || n <= 0 {
		return 0, 0, 0, pxePgpCorruptArmor
	}
	p := start + n
	armorEnd, n, err = armorFindHeader(data, p, true)
	if err != nil || n <= 0 {
		return 0, 0, 0, pxePgpCorruptArmor
	}
	hdrStart = p
	for p < armorEnd && data[p] != '\n' && data[p] != '\r' {
		i := bytes.IndexByte(data[p:armorEnd], '\n')
		if i < 0 {
			return 0, 0, 0, pxePgpCorruptArmor
		}
		p += i + 1
	}
	return hdrStart, p, armorEnd, nil
}

// armorDecode is pgp_armor_decode.
func armorDecode(data []byte) ([]byte, error) {
	_, b64Start, armorEnd, err := armorBody(data)
	if err != nil {
		return nil, err
	}
	p := armorEnd
	for ; p >= b64Start; p-- {
		if data[p] == '=' {
			break
		}
	}
	if p < b64Start {
		return nil, pxePgpCorruptArmor
	}
	crcBytes, err := armorBase64Decode(data[p+1 : p+5])
	if err != nil || len(crcBytes) != 3 {
		return nil, pxePgpCorruptArmor
	}
	crc := uint32(crcBytes[0])<<16 | uint32(crcBytes[1])<<8 | uint32(crcBytes[2])
	res, err := armorBase64Decode(data[b64Start:max(p-1, b64Start)])
	if err != nil {
		return nil, err
	}
	if armorCRC24(res) != crc {
		return nil, pxePgpCorruptArmor
	}
	return res, nil
}

// armorHeaders is pgp_extract_armor_headers.
func armorHeaders(data []byte) (keys, values []string, err error) {
	hdrStart, b64Start, _, err := armorBody(data)
	if err != nil {
		return nil, nil, err
	}
	hdrs := string(data[hdrStart:b64Start])
	for {
		eol := strings.IndexByte(hdrs, '\n')
		if eol < 0 {
			break
		}
		line := hdrs[:eol]
		hdrs = hdrs[eol+1:]
		line = strings.TrimSuffix(line, "\r")
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			return nil, nil, pxePgpCorruptArmor
		}
		keys = append(keys, k)
		values = append(values, v)
	}
	return keys, values, nil
}
