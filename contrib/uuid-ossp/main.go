// Package main is a port of PostgreSQL's contrib/uuid-ossp (version 1.1):
// functions generating UUIDs of versions 1, 3, 4 and 5 (RFC 4122), and the
// nil and namespace UUIDs.
package main

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"time"

	"gopgext/pg"
)

// The constant UUIDs of RFC 4122.
var (
	nsDNS  = mustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	nsURL  = mustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8")
	nsOID  = mustParse("6ba7b812-9dad-11d1-80b4-00c04fd430c8")
	nsX500 = mustParse("6ba7b814-9dad-11d1-80b4-00c04fd430c8")
)

func mustParse(s string) pg.UUID {
	u, err := pg.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

//gopgext:function immutable parallel=safe symbol=uuid_nil
func UuidNil() pg.UUID { return pg.UUID{} }

//gopgext:function immutable parallel=safe symbol=uuid_ns_dns
func UuidNsDns() pg.UUID { return nsDNS }

//gopgext:function immutable parallel=safe symbol=uuid_ns_url
func UuidNsUrl() pg.UUID { return nsURL }

//gopgext:function immutable parallel=safe symbol=uuid_ns_oid
func UuidNsOid() pg.UUID { return nsOID }

//gopgext:function immutable parallel=safe symbol=uuid_ns_x500
func UuidNsX500() pg.UUID { return nsX500 }

// random fills b from PostgreSQL's strong random source.
func random(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
}

// The state of version 1 UUIDs, per backend: the node (a random multicast
// address: the MAC address isn't available), the clock sequence and the
// last timestamp, which only increases.
var (
	node     [6]byte
	clockSeq uint16
	lastTime uint64
)

func init() {
	random(node[:])
	node[0] |= 0x01 // multicast: not a real MAC address (RFC 4122, 4.5)
	var b [2]byte
	random(b[:])
	clockSeq = binary.BigEndian.Uint16(b[:]) & 0x3fff
}

// gregorianOffset is the number of 100 ns intervals between 1582-10-15 (the
// epoch of version 1 timestamps) and 1970-01-01.
const gregorianOffset = 0x01B21DD213814000

// v1 returns a time-based UUID with the given node.
func v1(node [6]byte) pg.UUID {
	ts := uint64(time.Now().UnixNano()/100) + gregorianOffset
	if ts <= lastTime {
		ts = lastTime + 1
	}
	lastTime = ts
	var u pg.UUID
	binary.BigEndian.PutUint32(u[0:], uint32(ts))
	binary.BigEndian.PutUint16(u[4:], uint16(ts>>32))
	binary.BigEndian.PutUint16(u[6:], uint16(ts>>48)&0x0fff|0x1000)
	u[8] = byte(clockSeq>>8)&0x3f | 0x80
	u[9] = byte(clockSeq)
	copy(u[10:], node[:])
	return u
}

//gopgext:function parallel=safe symbol=uuid_generate_v1
func UuidGenerateV1() pg.UUID { return v1(node) }

//gopgext:function parallel=safe symbol=uuid_generate_v1mc
func UuidGenerateV1mc() pg.UUID {
	var n [6]byte
	random(n[:])
	n[0] |= 0x03 // multicast and locally administered, like the original
	return v1(n)
}

// v35 returns a name-based UUID: sum is the hash of the namespace and name.
func v35(sum []byte, version byte) pg.UUID {
	var u pg.UUID
	copy(u[:], sum)
	u[6] = u[6]&0x0f | version<<4
	u[8] = u[8]&0x3f | 0x80
	return u
}

//gopgext:function immutable parallel=safe symbol=uuid_generate_v3
func UuidGenerateV3(namespace pg.UUID, name string) pg.UUID {
	h := md5.New()
	h.Write(namespace[:])
	h.Write([]byte(name))
	return v35(h.Sum(nil), 3)
}

//gopgext:function parallel=safe symbol=uuid_generate_v4
func UuidGenerateV4() pg.UUID {
	var u pg.UUID
	random(u[:])
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return u
}

//gopgext:function immutable parallel=safe symbol=uuid_generate_v5
func UuidGenerateV5(namespace pg.UUID, name string) pg.UUID {
	h := sha1.New()
	h.Write(namespace[:])
	h.Write([]byte(name))
	return v35(h.Sum(nil), 5)
}

func main() {}
