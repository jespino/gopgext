package main

// OpenPGP decryption (pgp-decrypt.c): packet parsing, the decryption,
// prefix check and MDC filters, literal and compressed data.

import "bytes"

const (
	pgpPktNormal  = 1
	pgpPktStream  = 2
	pgpPktContext = 3

	pgpMaxChunk = 16 * 1024 * 1024
)

// pgpParseNewLen is parse_new_len: returns the length and packet type.
func pgpParseNewLen(src *pullFilter) (int, int, error) {
	b, err := src.getByte()
	if err != nil {
		return 0, 0, err
	}
	var n int
	typ := pgpPktNormal
	switch {
	case b <= 191:
		n = int(b)
	case b <= 223:
		n = (int(b) - 192) << 8
		if b, err = src.getByte(); err != nil {
			return 0, 0, err
		}
		n += 192 + int(b)
	case b == 255:
		var v uint32
		for i := 0; i < 4; i++ {
			if b, err = src.getByte(); err != nil {
				return 0, 0, err
			}
			v = v<<8 | uint32(b)
		}
		n = int(int32(v))
	default:
		n = 1 << (b & 0x1f)
		typ = pgpPktStream
	}
	if n < 0 || n > pgpMaxChunk {
		pgpDebug("parse_new_len: weird length")
		return 0, 0, pxePgpCorruptData
	}
	return n, typ, nil
}

func pgpParseOldLen(src *pullFilter, lentype int) (int, error) {
	b, err := src.getByte()
	if err != nil {
		return 0, err
	}
	n := int(b)
	extra := 0
	switch lentype {
	case 1:
		extra = 1
	case 2:
		extra = 3
	}
	for i := 0; i < extra; i++ {
		if b, err = src.getByte(); err != nil {
			return 0, err
		}
		n = n<<8 | int(b)
	}
	n = int(int32(uint32(n)))
	if n < 0 || n > pgpMaxChunk {
		pgpDebug("parse_old_len: weird length")
		return 0, pxePgpCorruptData
	}
	return n, nil
}

// pgpParsePktHdr is pgp_parse_pkt_hdr: tag, length and packet type (0 at
// the end of the input).
func pgpParsePktHdr(src *pullFilter, allowCtx bool) (tag byte, n int, typ int, err error) {
	p, err := src.read(1)
	if err != nil || len(p) == 0 {
		return 0, 0, 0, err
	}
	if p[0]&0x80 == 0 {
		pgpDebug("pgp_parse_pkt_hdr: not pkt hdr")
		return 0, 0, 0, pxePgpCorruptData
	}
	h := p[0]
	if h&0x40 != 0 {
		tag = h & 0x3f
		n, typ, err = pgpParseNewLen(src)
		return
	}
	lentype := int(h & 3)
	tag = (h >> 2) & 0x0f
	if lentype == 3 {
		if !allowCtx {
			return tag, 0, 0, pxePgpCorruptData
		}
		return tag, 0, pgpPktContext, nil
	}
	n, err = pgpParseOldLen(src, lentype)
	return tag, n, pgpPktNormal, err
}

// pgpPktReader is pgp_create_pkt_reader: the body of a packet.
func pgpPktReader(src *pullFilter, n, typ int) *pullFilter {
	return &pullFilter{src: src, pull: func(src *pullFilter, want int) ([]byte, error) {
		if typ == pgpPktContext {
			return src.read(want)
		}
		for n == 0 {
			if typ == pgpPktNormal {
				return nil, nil
			}
			var err error
			if n, typ, err = pgpParseNewLen(src); err != nil {
				return nil, err
			}
		}
		want = min(want, n)
		d, err := src.read(want)
		if err == nil {
			n -= len(d)
		}
		return d, err
	}}
}

// pgpPrefixCheck is prefix_init: reads the random prefix and its "quick
// check" bytes (a failure is reported at the end, by pgpDecrypt).
func pgpPrefixCheck(ctx *pgpContext, src *pullFilter) (*pullFilter, error) {
	n := pgpCipherBlockSize(ctx.cipherAlgo)
	buf, err := src.readMax(n + 2)
	if err != nil {
		return nil, err
	}
	if len(buf) != n+2 {
		pgpDebug("prefix_init: short read")
		return nil, pxePgpCorruptData
	}
	if buf[n-2] != buf[n] || buf[n-1] != buf[n+1] {
		pgpDebug("prefix_init: corrupt prefix")
		ctx.corruptPrefix = true
	}
	return &pullFilter{src: src}, nil
}

// pgpDecryptFilter is pgp_decrypt_filter.
func pgpDecryptFilter(cfb *pgpCFB, src *pullFilter) *pullFilter {
	return &pullFilter{src: src, buflen: 4096, pull: func(src *pullFilter, n int) ([]byte, error) {
		d, err := src.read(n)
		if err != nil || len(d) == 0 {
			return d, err
		}
		out := make([]byte, len(d))
		if err := cfb.decrypt(d, out); err != nil {
			return nil, err
		}
		return out, nil
	}}
}

// pgpMDCFilter is mdc_filter: hashes what's read (until the MDC packet).
func pgpMDCFilter(ctx *pgpContext, src *pullFilter) (*pullFilter, error) {
	md, err := pgpLoadDigest(pgpDigestSHA1)
	if err != nil {
		return nil, err
	}
	ctx.mdc = md
	return &pullFilter{src: src, pull: func(src *pullFilter, n int) ([]byte, error) {
		if ctx.useMDCBufFilter || ctx.inMDCPkt {
			return src.read(n)
		}
		d, err := src.read(n)
		if err != nil {
			return nil, err
		}
		if len(d) == 0 {
			pgpDebug("mdc_read: unexpected eof")
			return nil, pxePgpCorruptData
		}
		ctx.mdc.Write(d)
		return d, nil
	}}, nil
}

// pgpMDCFinish is mdc_finish: checks the MDC packet.
func pgpMDCFinish(ctx *pgpContext, src *pullFilter, n int) error {
	if ctx.useMDCBufFilter {
		return pxeBug
	}
	if n != 20 {
		return pxePgpCorruptData
	}
	ctx.inMDCPkt = true
	d, err := src.readMax(n)
	if err != nil {
		return err
	}
	if len(d) == 0 {
		pgpDebug("no mdc")
		return pxePgpCorruptData
	}
	if len(d) != 20 {
		pgpDebug("mdc_finish: read failed, res=%d", len(d))
		return pxePgpCorruptData
	}
	if !bytes.Equal(ctx.mdc.Sum(nil), d) {
		pgpDebug("mdc_finish: mdc failed")
		return pxePgpCorruptData
	}
	return nil
}

const pgpMDCBufLen = 8192

// pgpMDCBuf is mdcbuf_filter: for an encrypted data packet with a context
// length, holds back the last 22 bytes (the MDC packet) and checks them
// at the end.
type pgpMDCBuf struct {
	ctx    *pgpContext
	eof    bool
	data   []byte // available data
	mdcBuf []byte
}

func newPGPMDCBuf(ctx *pgpContext, src *pullFilter) *pullFilter {
	st := &pgpMDCBuf{ctx: ctx}
	ctx.useMDCBufFilter = true
	return &pullFilter{src: src, pull: func(src *pullFilter, n int) ([]byte, error) {
		if !st.eof && n > len(st.data) {
			if err := st.refill(src); err != nil {
				return nil, err
			}
		}
		n = min(n, len(st.data))
		d := st.data[:n]
		st.data = st.data[n:]
		return d, nil
	}}
}

func (st *pgpMDCBuf) finish() error {
	st.eof = true
	if len(st.mdcBuf) < 2 || st.mdcBuf[0] != 0xd3 || st.mdcBuf[1] != 0x14 {
		pgpDebug("mdcbuf_finish: bad MDC pkt hdr")
		return pxePgpCorruptData
	}
	st.ctx.mdc.Write(st.mdcBuf[:2])
	// A short MDC compares against the zeros of the C buffer.
	var want [20]byte
	copy(want[:], st.mdcBuf[2:])
	if !bytes.Equal(st.ctx.mdc.Sum(nil), want[:]) {
		pgpDebug("mdcbuf_finish: MDC does not match")
		return pxePgpCorruptData
	}
	return nil
}

func (st *pgpMDCBuf) load(d []byte) {
	st.data = append(st.data, d...)
	st.ctx.mdc.Write(d)
}

func (st *pgpMDCBuf) refill(src *pullFilter) error {
	st.data = append([]byte(nil), st.data...)
	need := pgpMDCBufLen + 22 - len(st.data) - len(st.mdcBuf)
	d, err := src.read(need)
	if err != nil {
		return err
	}
	if len(d) == 0 {
		return st.finish()
	}
	if len(d) >= 22 {
		st.load(st.mdcBuf)
		st.load(d[:len(d)-22])
		st.mdcBuf = append([]byte(nil), d[len(d)-22:]...)
	} else {
		if canmove := len(st.mdcBuf) + len(d) - 22; canmove > 0 {
			st.load(st.mdcBuf[:canmove])
			st.mdcBuf = st.mdcBuf[canmove:]
		}
		st.mdcBuf = append(append([]byte(nil), st.mdcBuf...), d...)
	}
	return nil
}

// pgpDecryptKey is decrypt_key: a session key encrypted with the S2K key.
func pgpDecryptKey(ctx *pgpContext, src []byte) error {
	cfb, err := newPGPCFB(ctx.s2kCipherAlgo, ctx.s2k.key, false, nil, ctx.ignoreCipherFailure)
	if err != nil {
		return err
	}
	var algo [1]byte
	if err := cfb.decrypt(src[:1], algo[:]); err != nil {
		return err
	}
	ctx.sessKey = make([]byte, len(src)-1)
	if err := cfb.decrypt(src[1:], ctx.sessKey); err != nil {
		return err
	}
	ctx.cipherAlgo = int(algo[0])
	if pgpCipherKeySize(ctx.cipherAlgo) != len(ctx.sessKey) {
		pgpDebug("sesskey bad len: algo=%d, expected=%d, got=%d",
			ctx.cipherAlgo, pgpCipherKeySize(ctx.cipherAlgo), len(ctx.sessKey))
		return pxePgpCorruptData
	}
	return nil
}

// pgpParseSymencSesskey is parse_symenc_sesskey.
func pgpParseSymencSesskey(ctx *pgpContext, src *pullFilter) error {
	ver, err := src.getByte()
	if err != nil {
		return err
	}
	algo, err := src.getByte()
	if err != nil {
		return err
	}
	ctx.s2kCipherAlgo = int(algo)
	if ver != 4 {
		pgpDebug("bad key pkt ver")
		return pxePgpCorruptData
	}
	if err := pgpS2KRead(src, &ctx.s2k); err != nil {
		return err
	}
	ctx.s2kMode = int(ctx.s2k.mode)
	ctx.s2kCount = s2kDecodeCount(ctx.s2k.iter)
	ctx.s2kDigestAlgo = int(ctx.s2k.digestAlgo)
	if err := pgpS2KProcess(&ctx.s2k, ctx.s2kCipherAlgo, ctx.symKey); err != nil {
		return err
	}
	p, err := src.readMax(pgpMaxKey + 2)
	if err != nil {
		return err
	}
	if len(p) == 0 {
		ctx.sessKey = append([]byte(nil), ctx.s2k.key...)
		ctx.cipherAlgo = ctx.s2kCipherAlgo
		ctx.useSessKey = 0
		return nil
	}
	if len(p) < 17 || len(p) > pgpMaxKey+1 {
		pgpDebug("expect key, but bad data")
		return pxePgpCorruptData
	}
	ctx.useSessKey = 1
	return pgpDecryptKey(ctx, p)
}

// pgpCopyCRLF is copy_crlf: appends data with CRLF turned into LF.
func pgpCopyCRLF(dst *[]byte, data []byte, gotCR *bool) {
	if *gotCR {
		if data[0] != '\n' {
			*dst = append(*dst, '\r')
		}
		*gotCR = false
	}
	for i := 0; i < len(data); i++ {
		if data[i] == '\r' {
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					i++
				}
			} else {
				*gotCR = true
				break
			}
		}
		*dst = append(*dst, data[i])
	}
}

// pgpParseLiteralData is parse_literal_data.
func pgpParseLiteralData(ctx *pgpContext, dst *[]byte, pkt *pullFilter) error {
	typ, err := pkt.getByte()
	if err != nil {
		return err
	}
	nameLen, err := pkt.getByte()
	if err != nil {
		return err
	}
	n := int(nameLen)
	for n > 0 {
		d, err := pkt.read(n)
		if err != nil {
			return err
		}
		if len(d) == 0 {
			break
		}
		n -= len(d)
	}
	if n > 0 {
		pgpDebug("parse_literal_data: unexpected eof")
		return pxePgpCorruptData
	}
	if d, err := pkt.readMax(4); err != nil || len(d) != 4 {
		pgpDebug("parse_literal_data: unexpected eof")
		return pxePgpCorruptData
	}
	if ctx.textMode && typ != 't' && typ != 'u' {
		pgpDebug("parse_literal_data: data type=%c", rune(typ))
		ctx.unexpectedBinary = true
	}
	ctx.unicodeMode = b2i(typ == 'u')
	gotCR := false
	for {
		d, err := pkt.read(32 * 1024)
		if err != nil {
			return err
		}
		if len(d) == 0 {
			break
		}
		if ctx.textMode && ctx.convertCRLF {
			pgpCopyCRLF(dst, d, &gotCR)
		} else {
			*dst = append(*dst, d...)
		}
	}
	if gotCR {
		*dst = append(*dst, '\r')
	}
	return nil
}

// pgpParseCompressedData is parse_compressed_data.
func pgpParseCompressedData(ctx *pgpContext, dst *[]byte, pkt *pullFilter) error {
	typ, err := pkt.getByte()
	if err != nil {
		return err
	}
	ctx.compressAlgo = int(typ)
	switch typ {
	case pgpComprNone:
		return pgpProcessDataPackets(ctx, dst, pkt, false, false)
	case pgpComprZIP, pgpComprZLIB:
		pf, err := pgpDecompressFilter(ctx, pkt)
		if err != nil {
			return err
		}
		return pgpProcessDataPackets(ctx, dst, pf, false, false)
	case pgpComprBZIP2:
		pgpDebug("parse_compressed_data: bzip2 unsupported")
		ctx.unsupportedCompr = true
		for {
			d, err := pkt.read(32 * 1024)
			if err != nil || len(d) == 0 {
				return err
			}
		}
	}
	pgpDebug("parse_compressed_data: unknown compr type")
	return pxePgpCorruptData
}

// pgpProcessDataPackets is process_data_packets.
func pgpProcessDataPackets(ctx *pgpContext, dst *[]byte, src *pullFilter, allowCompr, needMDC bool) error {
	gotData, gotMDC := false, false
	var err error
	for {
		var tag byte
		var n, typ int
		tag, n, typ, err = pgpParsePktHdr(src, true)
		if err != nil || typ == 0 {
			break
		}
		if gotMDC {
			pgpDebug("process_data_packets: data after mdc")
			err = pxePgpCorruptData
			break
		}
		var pkt *pullFilter
		if needMDC && typ == pgpPktContext {
			pkt = newPGPMDCBuf(ctx, src)
		} else {
			pkt = pgpPktReader(src, n, typ)
		}
		switch tag {
		case pgpPktLiteralData:
			gotData = true
			err = pgpParseLiteralData(ctx, dst, pkt)
		case pgpPktCompressedData:
			switch {
			case !allowCompr:
				pgpDebug("process_data_packets: unexpected compression")
				err = pxePgpCorruptData
			case gotData:
				pgpDebug("process_data_packets: only one cmpr pkt allowed")
				err = pxePgpCorruptData
			default:
				gotData = true
				err = pgpParseCompressedData(ctx, dst, pkt)
			}
		case pgpPktMDC:
			if !needMDC {
				pgpDebug("process_data_packets: unexpected MDC")
				err = pxePgpCorruptData
				break
			}
			if err = pgpMDCFinish(ctx, pkt, n); err == nil {
				gotMDC = true
			}
		default:
			pgpDebug("process_data_packets: unexpected pkt tag=%d", tag)
			err = pxePgpCorruptData
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		return err
	}
	if !gotData {
		pgpDebug("process_data_packets: no data")
		err = pxePgpCorruptData
	}
	if needMDC && !gotMDC && !ctx.useMDCBufFilter {
		pgpDebug("process_data_packets: got no mdc")
		err = pxePgpCorruptData
	}
	return err
}

// pgpParseSymencData is parse_symenc_data (no MDC, resyncing CFB).
func pgpParseSymencData(ctx *pgpContext, pkt *pullFilter, dst *[]byte) error {
	cfb, err := newPGPCFB(ctx.cipherAlgo, ctx.sessKey, true, nil, ctx.ignoreCipherFailure)
	if err != nil {
		return err
	}
	pf, err := pgpPrefixCheck(ctx, pgpDecryptFilter(cfb, pkt))
	if err != nil {
		return err
	}
	return pgpProcessDataPackets(ctx, dst, pf, true, false)
}

// pgpParseSymencMDCData is parse_symenc_mdc_data.
func pgpParseSymencMDCData(ctx *pgpContext, pkt *pullFilter, dst *[]byte) error {
	ver, err := pkt.getByte()
	if err != nil {
		return err
	}
	if ver != 1 {
		pgpDebug("parse_symenc_mdc_data: pkt ver != 1")
		return pxePgpCorruptData
	}
	cfb, err := newPGPCFB(ctx.cipherAlgo, ctx.sessKey, false, nil, ctx.ignoreCipherFailure)
	if err != nil {
		return err
	}
	pfMDC, err := pgpMDCFilter(ctx, pgpDecryptFilter(cfb, pkt))
	if err != nil {
		return err
	}
	pf, err := pgpPrefixCheck(ctx, pfMDC)
	if err != nil {
		return err
	}
	return pgpProcessDataPackets(ctx, dst, pf, true, true)
}

// pgpSkipPacket is pgp_skip_packet.
func pgpSkipPacket(pkt *pullFilter) error {
	for {
		d, err := pkt.read(32 * 1024)
		if err != nil || len(d) == 0 {
			return err
		}
	}
}

// pgpExpectPacketEnd is pgp_expect_packet_end.
func pgpExpectPacketEnd(pkt *pullFilter) error {
	d, err := pkt.read(32 * 1024)
	if err == nil && len(d) > 0 {
		pgpDebug("pgp_expect_packet_end: got data")
		return pxePgpCorruptData
	}
	return err
}

// pgpDecrypt is pgp_decrypt.
func pgpDecrypt(ctx *pgpContext, msg []byte, dst *[]byte) error {
	src := newBytesReader(msg)
	gotKey, gotData := false, false
	var err error
	for err == nil {
		var tag byte
		var n, typ int
		tag, n, typ, err = pgpParsePktHdr(src, false)
		if err != nil || typ == 0 {
			break
		}
		pkt := pgpPktReader(src, n, typ)
		err = pxePgpCorruptData
		switch tag {
		case pgpPktMarker:
			err = pgpSkipPacket(pkt)
		case pgpPktPubencryptedSesskey:
			err = pgpParsePubencSesskey(ctx, pkt)
			gotKey = true
		case pgpPktSymencryptedSesskey:
			if gotKey {
				pgpDebug("pgp_decrypt: using first of several keys")
			} else {
				gotKey = true
				err = pgpParseSymencSesskey(ctx, pkt)
			}
		case pgpPktSymencryptedData:
			switch {
			case !gotKey:
				pgpDebug("pgp_decrypt: have data but no key")
			case gotData:
				pgpDebug("pgp_decrypt: got second data packet")
			default:
				gotData = true
				ctx.disableMDC = 1
				err = pgpParseSymencData(ctx, pkt, dst)
			}
		case pgpPktSymencryptedDataMDC:
			switch {
			case !gotKey:
				pgpDebug("pgp_decrypt: have data but no key")
			case gotData:
				pgpDebug("pgp_decrypt: several data pkts not supported")
			default:
				gotData = true
				ctx.disableMDC = 0
				err = pgpParseSymencMDCData(ctx, pkt, dst)
			}
		default:
			pgpDebug("pgp_decrypt: unknown tag: 0x%02x", tag)
		}
	}
	if err != nil {
		return err
	}
	if !gotData || ctx.corruptPrefix {
		return pxePgpCorruptData
	}
	if ctx.unsupportedCompr {
		return pxePgpUnsupportedCompr
	}
	if ctx.unexpectedBinary {
		return pxePgpNotText
	}
	return nil
}
