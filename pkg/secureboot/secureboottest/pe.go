// Package secureboottest builds the smallest PE image debug/pe accepts that
// still carries a ".vendor_cert" section, so shim parsing can be tested
// without checking in a real 950 KB shim.
package secureboottest

import (
	"bytes"
	"encoding/binary"
)

// MinimalShim returns an x86-64 PE with a single section named through the
// COFF string table (as shim's ".vendor_cert" must be, the name being
// longer than 8 bytes) whose raw data is section.
func MinimalShim(section []byte) []byte {
	const (
		peOffset     = 64
		coffOffset   = peOffset + 4
		sectionTable = coffOffset + 20
		stringTable  = sectionTable + 40
	)
	name := ".vendor_cert\x00"
	strtabSize := 4 + len(name)
	rawOffset := (stringTable + strtabSize + 511) &^ 511
	rawSize := (len(section) + 511) &^ 511

	var b bytes.Buffer
	dos := make([]byte, peOffset)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], peOffset)
	b.Write(dos)
	b.WriteString("PE\x00\x00")

	coff := make([]byte, 20)
	binary.LittleEndian.PutUint16(coff[0:], 0x8664)
	binary.LittleEndian.PutUint16(coff[2:], 1)
	binary.LittleEndian.PutUint32(coff[8:], stringTable)
	binary.LittleEndian.PutUint32(coff[12:], 0)
	binary.LittleEndian.PutUint16(coff[16:], 0)
	binary.LittleEndian.PutUint16(coff[18:], 0x2002)
	b.Write(coff)

	sec := make([]byte, 40)
	copy(sec, "/4")
	binary.LittleEndian.PutUint32(sec[8:], uint32(len(section)))
	binary.LittleEndian.PutUint32(sec[12:], 0x1000)
	binary.LittleEndian.PutUint32(sec[16:], uint32(rawSize))
	binary.LittleEndian.PutUint32(sec[20:], uint32(rawOffset))
	binary.LittleEndian.PutUint32(sec[36:], 0x40000040)
	b.Write(sec)

	strtab := make([]byte, 4)
	binary.LittleEndian.PutUint32(strtab, uint32(strtabSize))
	b.Write(strtab)
	b.WriteString(name)

	b.Write(make([]byte, rawOffset-b.Len()))
	b.Write(section)
	b.Write(make([]byte, rawSize-len(section)))
	return b.Bytes()
}
