package main

// Streams for the OpenPGP code (mbuf.c): pull filters read a message
// through a chain of decoders (packets, decryption, decompression), push
// filters write one through a chain of encoders. A read returns at most
// the bytes asked for; an empty result is the end of the stream.

// pullFilter is a PullFilter.
type pullFilter struct {
	src *pullFilter
	// pull reads up to n bytes from src; nil passes reads through.
	pull func(src *pullFilter, n int) ([]byte, error)
	// buflen limits reads (filters with an output buffer).
	buflen int
}

// read is pullf_read.
func (pf *pullFilter) read(n int) ([]byte, error) {
	if pf.pull == nil {
		return pf.src.read(n)
	}
	if pf.buflen > 0 && n > pf.buflen {
		n = pf.buflen
	}
	return pf.pull(pf.src, n)
}

// readMax is pullf_read_max: up to n bytes, reading again on short reads.
func (pf *pullFilter) readMax(n int) ([]byte, error) {
	d, err := pf.read(n)
	if err != nil || len(d) == 0 || len(d) == n {
		return d, err
	}
	buf := append([]byte(nil), d...)
	for len(buf) < n {
		d, err = pf.read(n - len(buf))
		if err != nil {
			return nil, err
		}
		if len(d) == 0 {
			break
		}
		buf = append(buf, d...)
	}
	return buf, nil
}

// readFixed is pullf_read_fixed: exactly n bytes.
func (pf *pullFilter) readFixed(n int) ([]byte, error) {
	d, err := pf.readMax(n)
	if err != nil {
		return nil, err
	}
	if len(d) != n {
		pgpDebug("pullf_read_fixed: need=%d got=%d", n, len(d))
		return nil, pxePgpCorruptData
	}
	return d, nil
}

// getByte is GETBYTE.
func (pf *pullFilter) getByte() (byte, error) {
	d, err := pf.readFixed(1)
	if err != nil {
		return 0, err
	}
	return d[0], nil
}

// newBytesReader is pullf_create_mbuf_reader.
func newBytesReader(data []byte) *pullFilter {
	return &pullFilter{pull: func(_ *pullFilter, n int) ([]byte, error) {
		if n > len(data) {
			n = len(data)
		}
		d := data[:n]
		data = data[n:]
		return d, nil
	}}
}

// pushFilter is a PushFilter.
type pushFilter struct {
	next *pushFilter
	// push processes a block; nil passes it to next.
	push func(next *pushFilter, data []byte) error
	// flush ends the stream (after the buffered data).
	flush func(next *pushFilter) error
	// buf collects data into blocks of len(buf) (if not nil).
	buf []byte
	pos int
}

func newPushFilter(next *pushFilter, blockSize int, push func(*pushFilter, []byte) error,
	flush func(*pushFilter) error) *pushFilter {
	mp := &pushFilter{next: next, push: push, flush: flush}
	if blockSize > 0 {
		mp.buf = make([]byte, blockSize)
	}
	return mp
}

func (mp *pushFilter) process(data []byte) error {
	if mp.push != nil {
		return mp.push(mp.next, data)
	}
	return mp.next.write(data)
}

// write is pushf_write: consumes all of data.
func (mp *pushFilter) write(data []byte) error {
	bs := len(mp.buf)
	if bs == 0 {
		return mp.process(data)
	}
	need := bs - mp.pos
	if need > 0 {
		if len(data) < need {
			copy(mp.buf[mp.pos:], data)
			mp.pos += len(data)
			return nil
		}
		copy(mp.buf[mp.pos:], data[:need])
		data = data[need:]
	}
	if err := mp.process(mp.buf); err != nil {
		return err
	}
	mp.pos = 0
	for len(data) > 0 {
		if len(data) > bs {
			if err := mp.process(data[:bs]); err != nil {
				return err
			}
			data = data[bs:]
		} else {
			copy(mp.buf, data)
			mp.pos += len(data)
			break
		}
	}
	return nil
}

// flushAll is pushf_flush: processes buffered data and ends every filter
// of the chain.
func (mp *pushFilter) flushAll() error {
	for ; mp != nil; mp = mp.next {
		if mp.buf != nil {
			if err := mp.process(mp.buf[:mp.pos]); err != nil {
				return err
			}
		}
		if mp.flush != nil {
			if err := mp.flush(mp.next); err != nil {
				return err
			}
		}
	}
	return nil
}

// newBytesWriter is pushf_create_mbuf_writer.
func newBytesWriter(dst *[]byte) *pushFilter {
	return newPushFilter(nil, 0, func(_ *pushFilter, data []byte) error {
		*dst = append(*dst, data...)
		return nil
	}, nil)
}
