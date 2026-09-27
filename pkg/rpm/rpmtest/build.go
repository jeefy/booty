// Package rpmtest assembles small synthetic RPM packages (lead, signature
// header, header, compressed newc cpio payload) for tests of code that
// reads RPMs; it deliberately shares no code with package rpm.
package rpmtest

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// RPM header tags and the string type, as in rpmtag.h.
const (
	TagPayloadFormat     int32 = 1124
	TagPayloadCompressor int32 = 1125
	typeString                 = 6
)

// Member is one cpio entry. Mode is the st_mode value (0o100644 regular
// file, 0o040755 directory, 0o120777 symlink).
type Member struct {
	Name string
	Mode uint32
	Data []byte
}

// Newc encodes members as a newc cpio archive ending in TRAILER!!!.
func Newc(members ...Member) []byte {
	var b bytes.Buffer
	write := func(m Member) {
		fmt.Fprintf(&b, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			1, m.Mode, 0, 0, 1, 0, len(m.Data), 0, 0, 0, 0, len(m.Name)+1, 0)
		b.WriteString(m.Name)
		b.WriteByte(0)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
		b.Write(m.Data)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	for _, m := range members {
		write(m)
	}
	write(Member{Name: "TRAILER!!!"})
	return b.Bytes()
}

// Header encodes one RPM header structure holding string tags.
func Header(entries map[int32]string) []byte {
	var index, store bytes.Buffer
	for tag, value := range entries {
		var e [16]byte
		binary.BigEndian.PutUint32(e[0:4], uint32(tag))
		binary.BigEndian.PutUint32(e[4:8], typeString)
		binary.BigEndian.PutUint32(e[8:12], uint32(store.Len()))
		binary.BigEndian.PutUint32(e[12:16], 1)
		index.Write(e[:])
		store.WriteString(value)
		store.WriteByte(0)
	}
	var b bytes.Buffer
	b.Write([]byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0})
	var n [8]byte
	binary.BigEndian.PutUint32(n[0:4], uint32(len(entries)))
	binary.BigEndian.PutUint32(n[4:8], uint32(store.Len()))
	b.Write(n[:])
	b.Write(index.Bytes())
	b.Write(store.Bytes())
	return b.Bytes()
}

// Compress encodes data with "zstd", "gzip" or, for any other name, not at
// all.
func Compress(algo string, data []byte) ([]byte, error) {
	var b bytes.Buffer
	switch algo {
	case "zstd":
		w, err := zstd.NewWriter(&b)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	case "gzip":
		w := gzip.NewWriter(&b)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	default:
		b.Write(data)
	}
	return b.Bytes(), nil
}

// Build returns a complete RPM: lead, a signature header of odd length (so
// the 8-byte alignment before the main header is exercised), the main
// header with headerTags and the payload compressed with compressor.
func Build(compressor string, headerTags map[int32]string, payload []byte) ([]byte, error) {
	var b bytes.Buffer
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb})
	b.Write(lead)
	b.Write(Header(map[int32]string{1000: "sigsize-odd"}))
	for b.Len()%8 != 0 {
		b.WriteByte(0)
	}
	b.Write(Header(headerTags))
	compressed, err := Compress(compressor, payload)
	if err != nil {
		return nil, err
	}
	b.Write(compressed)
	return b.Bytes(), nil
}

// Package is Build with the usual cpio/compressor tags and the given
// members, panicking on the impossible compression error.
func Package(compressor string, members ...Member) []byte {
	pkg, err := Build(compressor, map[int32]string{TagPayloadFormat: "cpio", TagPayloadCompressor: compressor}, Newc(members...))
	if err != nil {
		panic(err)
	}
	return pkg
}
