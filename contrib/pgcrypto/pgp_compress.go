package main

// Compressed data packets (pgp-compress.c): ZIP (raw deflate) and ZLIB,
// with compress/flate and compress/zlib.

import (
	"compress/flate"
	"compress/zlib"
	"errors"
	"io"
)

// pgpPushWriter is an io.Writer into a push filter.
type pgpPushWriter struct{ next *pushFilter }

func (w pgpPushWriter) Write(p []byte) (int, error) {
	if err := w.next.write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// pgpCompressFilter is pgp_compress_filter.
func pgpCompressFilter(ctx *pgpContext, dst *pushFilter) (*pushFilter, error) {
	var zw io.WriteCloser
	var err error
	switch ctx.compressAlgo {
	case pgpComprZIP:
		zw, err = flate.NewWriter(pgpPushWriter{dst}, ctx.compressLevel)
	case pgpComprZLIB:
		zw, err = zlib.NewWriterLevel(pgpPushWriter{dst}, ctx.compressLevel)
	default:
		return nil, pxePgpUnsupportedCompr
	}
	if err != nil {
		return nil, pxePgpCompressionError
	}
	unwrap := func(err error) error {
		var pe pxError
		if errors.As(err, &pe) {
			return pe
		}
		return pxePgpCompressionError
	}
	return newPushFilter(dst, 8192, func(_ *pushFilter, data []byte) error {
		if _, err := zw.Write(data); err != nil {
			return unwrap(err)
		}
		return nil
	}, func(_ *pushFilter) error {
		if err := zw.Close(); err != nil {
			return unwrap(err)
		}
		return nil
	}), nil
}

// pgpInflateSource feeds a decompressor from a pull filter, in reads of
// up to 8192 bytes like upstream's zlib loop.
type pgpInflateSource struct {
	src   *pullFilter
	chunk []byte
	err   error
}

func (r *pgpInflateSource) fill() error {
	for len(r.chunk) == 0 {
		if r.err != nil {
			return r.err
		}
		d, err := r.src.read(8192)
		if err != nil {
			r.err = err
		} else if len(d) == 0 {
			r.err = io.EOF
		}
		r.chunk = d
	}
	return nil
}

func (r *pgpInflateSource) Read(p []byte) (int, error) {
	if err := r.fill(); err != nil {
		return 0, err
	}
	n := copy(p, r.chunk)
	r.chunk = r.chunk[n:]
	return n, nil
}

func (r *pgpInflateSource) ReadByte() (byte, error) {
	if err := r.fill(); err != nil {
		return 0, err
	}
	b := r.chunk[0]
	r.chunk = r.chunk[1:]
	return b, nil
}

// pgpDecompressFilter is pgp_decompress_filter.
func pgpDecompressFilter(ctx *pgpContext, src *pullFilter) (*pullFilter, error) {
	if ctx.compressAlgo != pgpComprZLIB && ctx.compressAlgo != pgpComprZIP {
		return nil, pxePgpUnsupportedCompr
	}
	in := &pgpInflateSource{src: src}
	var zr io.Reader
	eof := false
	buf := make([]byte, 8192)
	var pending []byte
	return &pullFilter{src: src, pull: func(src *pullFilter, n int) ([]byte, error) {
		for len(pending) == 0 {
			if eof {
				return nil, nil
			}
			if zr == nil {
				if ctx.compressAlgo == pgpComprZIP {
					zr = flate.NewReader(in)
				} else {
					r, err := zlib.NewReader(in)
					if err != nil {
						return nil, pgpInflateError(err)
					}
					zr = r
				}
			}
			m, err := zr.Read(buf)
			pending = buf[:m]
			if err == io.EOF {
				// The stream must end with the packet: the rest of the
				// last chunk is ignored, as upstream.
				d, err := src.read(1)
				if err != nil {
					return nil, err
				}
				if len(d) > 0 {
					pgpDebug("decompress_read: extra bytes after end of stream")
					return nil, pxePgpCorruptData
				}
				eof = true
			} else if err != nil {
				return nil, pgpInflateError(err)
			}
		}
		n = min(n, len(pending))
		d := pending[:n]
		pending = pending[n:]
		return d, nil
	}}, nil
}

// pgpInflateError maps a decompression error to upstream's (with zlib's
// error code in the debug message).
func pgpInflateError(err error) error {
	var pe pxError
	if errors.As(err, &pe) {
		return pe
	}
	code := -3 // Z_DATA_ERROR
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		code = -5 // Z_BUF_ERROR
	case errors.Is(err, zlib.ErrDictionary):
		code = 2 // Z_NEED_DICT
	}
	pgpDebug("decompress_read: inflate error: %d", code)
	return pxePgpCorruptData
}
