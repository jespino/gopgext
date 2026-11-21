package main

// OpenPGP encryption (pgp-encrypt.c): a chain of push filters from the
// literal data packet down to the encrypted, streamed packets.

import (
	"bytes"
	"time"
)

const pgpStreamBlockShift = 14

// pgpRenderNewLen is render_newlen.
func pgpRenderNewLen(h []byte, n int) []byte {
	switch {
	case n <= 191:
		return append(h, byte(n))
	case n <= 8383:
		return append(h, byte((n-192)>>8+192), byte(n-192))
	}
	return append(h, 255, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

func pgpWriteTagOnly(dst *pushFilter, tag int) error {
	return dst.write([]byte{0xc0 | byte(tag)})
}

func pgpWriteNormalHeader(dst *pushFilter, tag, n int) error {
	return dst.write(pgpRenderNewLen([]byte{0xc0 | byte(tag)}, n))
}

// pgpMDCWriter is the MDC hasher: appends the MDC packet at the end.
func pgpMDCWriter(dst *pushFilter) (*pushFilter, error) {
	md, err := pgpLoadDigest(pgpDigestSHA1)
	if err != nil {
		return nil, err
	}
	return newPushFilter(dst, 0, func(next *pushFilter, data []byte) error {
		md.Write(data)
		return next.write(data)
	}, func(next *pushFilter) error {
		pkt := []byte{0xd3, 0x14}
		md.Write(pkt)
		return next.write(md.Sum(pkt))
	}), nil
}

// pgpEncryptWriter is encrypt_filter.
func pgpEncryptWriter(ctx *pgpContext, next *pushFilter) (*pushFilter, error) {
	resync := true
	if ctx.disableMDC == 0 {
		resync = false
		if err := next.write([]byte{1}); err != nil {
			return nil, err
		}
	}
	cfb, err := newPGPCFB(ctx.cipherAlgo, ctx.sessKey, resync, nil, false)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 8192)
	return newPushFilter(next, 8192, func(next *pushFilter, data []byte) error {
		for len(data) > 0 {
			n := min(len(data), len(buf))
			if err := cfb.encrypt(data[:n], buf); err != nil {
				return err
			}
			if err := next.write(buf[:n]); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}, nil), nil
}

// pgpPktStreamWriter is pkt_stream_filter: partial body lengths, ending
// with a normal length.
func pgpPktStreamWriter(dst *pushFilter) *pushFilter {
	finalDone := false
	block := 1 << pgpStreamBlockShift
	return newPushFilter(dst, block, func(next *pushFilter, data []byte) error {
		if finalDone {
			return pxeBug
		}
		var h []byte
		if len(data) == block {
			h = []byte{0xe0 | pgpStreamBlockShift}
		} else {
			h = pgpRenderNewLen(nil, len(data))
			finalDone = true
		}
		if err := next.write(h); err != nil {
			return err
		}
		return next.write(data)
	}, func(next *pushFilter) error {
		if !finalDone {
			if err := next.write(pgpRenderNewLen(nil, 0)); err != nil {
				return err
			}
			finalDone = true
		}
		return nil
	})
}

// pgpCreatePktWriter is pgp_create_pkt_writer.
func pgpCreatePktWriter(dst *pushFilter, tag int) (*pushFilter, error) {
	if err := pgpWriteTagOnly(dst, tag); err != nil {
		return nil, err
	}
	return pgpPktStreamWriter(dst), nil
}

// pgpCRLFWriter is crlf_filter: LF to CRLF.
func pgpCRLFWriter(dst *pushFilter) *pushFilter {
	return newPushFilter(dst, 0, func(next *pushFilter, data []byte) error {
		for len(data) > 0 {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				i = len(data)
			}
			if i > 0 {
				if err := next.write(data[:i]); err != nil {
					return err
				}
				data = data[i:]
			}
			for len(data) > 0 && data[0] == '\n' {
				if err := next.write([]byte{'\r', '\n'}); err != nil {
					return err
				}
				data = data[1:]
			}
		}
		return nil
	}, nil)
}

// pgpInitLitdataPacket is init_litdata_packet.
func pgpInitLitdataPacket(ctx *pgpContext, dst *pushFilter) (*pushFilter, error) {
	typ := byte('b')
	if ctx.textMode {
		typ = 't'
		if ctx.unicodeMode != 0 {
			typ = 'u'
		}
	}
	t := uint32(time.Now().Unix())
	hdr := []byte{typ, 0, byte(t >> 24), byte(t >> 16), byte(t >> 8), byte(t)}
	pkt, err := pgpCreatePktWriter(dst, pgpPktLiteralData)
	if err != nil {
		return nil, err
	}
	if err := pkt.write(hdr); err != nil {
		return nil, err
	}
	return pkt, nil
}

// pgpInitCompress is init_compress.
func pgpInitCompress(ctx *pgpContext, dst *pushFilter) (*pushFilter, error) {
	pkt, err := pgpCreatePktWriter(dst, pgpPktCompressedData)
	if err != nil {
		return nil, err
	}
	if err := pkt.write([]byte{byte(ctx.compressAlgo)}); err != nil {
		return nil, err
	}
	return pgpCompressFilter(ctx, pkt)
}

// pgpWritePrefix is write_prefix: random bytes and the "quick check".
func pgpWritePrefix(ctx *pgpContext, dst *pushFilter) error {
	bs := pgpCipherBlockSize(ctx.cipherAlgo)
	prefix := make([]byte, bs+2)
	if err := randomBytes(prefix[:bs]); err != nil {
		return pxeNoRandom
	}
	prefix[bs] = prefix[bs-2]
	prefix[bs+1] = prefix[bs-1]
	return dst.write(prefix)
}

// pgpWriteSymencSesskey is write_symenc_sesskey.
func pgpWriteSymencSesskey(ctx *pgpContext, dst *pushFilter) error {
	pkt := []byte{4, byte(ctx.s2kCipherAlgo), ctx.s2k.mode, ctx.s2k.digestAlgo}
	if ctx.s2k.mode > 0 {
		pkt = append(pkt, ctx.s2k.salt[:]...)
	}
	if ctx.s2k.mode == 3 {
		pkt = append(pkt, ctx.s2k.iter)
	}
	if ctx.useSessKey != 0 {
		cfb, err := newPGPCFB(ctx.s2kCipherAlgo, ctx.s2k.key, false, nil, false)
		if err != nil {
			return err
		}
		enc := make([]byte, 1+len(ctx.sessKey))
		if err := cfb.encrypt([]byte{byte(ctx.cipherAlgo)}, enc[:1]); err != nil {
			return err
		}
		if err := cfb.encrypt(ctx.sessKey, enc[1:]); err != nil {
			return err
		}
		pkt = append(pkt, enc...)
	}
	if err := pgpWriteNormalHeader(dst, pgpPktSymencryptedSesskey, len(pkt)); err != nil {
		return err
	}
	return dst.write(pkt)
}

// pgpEncrypt is pgp_encrypt.
func pgpEncrypt(ctx *pgpContext, src []byte, dst *[]byte) error {
	if ctx.symKey == nil && ctx.pubKey == nil {
		return pxeArgumentError
	}
	pf := newBytesWriter(dst)
	if ctx.symKey != nil {
		if ctx.s2kCipherAlgo < 0 {
			ctx.s2kCipherAlgo = ctx.cipherAlgo
		}
		if err := pgpS2KFill(&ctx.s2k, ctx.s2kMode, ctx.s2kDigestAlgo, ctx.s2kCount); err != nil {
			return err
		}
		if err := pgpS2KProcess(&ctx.s2k, ctx.s2kCipherAlgo, ctx.symKey); err != nil {
			return err
		}
	}
	if ctx.useSessKey != 0 || ctx.pubKey != nil {
		ctx.sessKey = make([]byte, pgpCipherKeySize(ctx.cipherAlgo))
		if err := randomBytes(ctx.sessKey); err != nil {
			return pxeNoRandom
		}
	} else {
		ctx.sessKey = append([]byte(nil), ctx.s2k.key...)
	}
	var err error
	if ctx.pubKey != nil {
		err = pgpWritePubencSesskey(ctx, pf)
	} else {
		err = pgpWriteSymencSesskey(ctx, pf)
	}
	if err != nil {
		return err
	}
	tag := pgpPktSymencryptedDataMDC
	if ctx.disableMDC != 0 {
		tag = pgpPktSymencryptedData
	}
	if pf, err = pgpCreatePktWriter(pf, tag); err != nil {
		return err
	}
	if pf, err = pgpEncryptWriter(ctx, pf); err != nil {
		return err
	}
	if ctx.disableMDC == 0 {
		if pf, err = pgpMDCWriter(pf); err != nil {
			return err
		}
	}
	if err := pgpWritePrefix(ctx, pf); err != nil {
		return err
	}
	if ctx.compressAlgo > 0 && ctx.compressLevel > 0 {
		if pf, err = pgpInitCompress(ctx, pf); err != nil {
			return err
		}
	}
	if pf, err = pgpInitLitdataPacket(ctx, pf); err != nil {
		return err
	}
	if ctx.textMode && ctx.convertCRLF {
		pf = pgpCRLFWriter(pf)
	}
	if err := pf.write(src); err != nil {
		return err
	}
	return pf.flushAll()
}
