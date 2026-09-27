// Package rpm reads the payload of an RPM package without librpm: the lead
// and the two headers are skipped or parsed just far enough to learn how
// the payload is compressed, and the cpio archive inside is exposed member
// by member. It exists so Booty can lift a few signed EFI binaries out of
// distribution packages (the Fedora shim and GRUB) with no new dependency
// beyond the zstd decoder already in the module graph.
package rpm

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	leadSize    = 96
	headerMagic = "\x8e\xad\xe8\x01"

	tagPayloadFormat     = 1124
	tagPayloadCompressor = 1125

	typeString = 6

	// maxHeaderBytes bounds the header store so a hostile file cannot make
	// the reader allocate arbitrarily; real headers are a few hundred KB.
	maxHeaderBytes = 32 << 20
)

var leadMagic = []byte{0xed, 0xab, 0xee, 0xdb}

// Header is what Payload learned from the RPM header before the archive.
type Header struct {
	PayloadFormat     string
	PayloadCompressor string
}

// Payload consumes the lead, signature header and main header from r and
// returns a reader over the decompressed cpio archive together with the
// header fields it used. Only zstd and gzip payloads are supported (Fedora
// 44/45 use zstd); anything else is reported so the caller can say why.
func Payload(r io.Reader) (io.Reader, Header, error) {
	br := &countingReader{r: r}
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(br, lead); err != nil {
		return nil, Header{}, fmt.Errorf("rpm lead: %w", err)
	}
	if !bytes.Equal(lead[:4], leadMagic) {
		return nil, Header{}, fmt.Errorf("rpm lead: bad magic %x", lead[:4])
	}

	if _, _, err := readHeader(br, nil); err != nil {
		return nil, Header{}, fmt.Errorf("rpm signature header: %w", err)
	}
	if pad := (8 - br.n%8) % 8; pad != 0 {
		if _, err := io.CopyN(io.Discard, br, int64(pad)); err != nil {
			return nil, Header{}, fmt.Errorf("rpm signature padding: %w", err)
		}
	}

	var h Header
	_, _, err := readHeader(br, func(tag, typ int32, value []byte) {
		if typ != typeString {
			return
		}
		s := string(bytes.TrimRight(value, "\x00"))
		switch tag {
		case tagPayloadFormat:
			h.PayloadFormat = s
		case tagPayloadCompressor:
			h.PayloadCompressor = s
		}
	})
	if err != nil {
		return nil, Header{}, fmt.Errorf("rpm header: %w", err)
	}
	if h.PayloadFormat != "" && h.PayloadFormat != "cpio" {
		return nil, h, fmt.Errorf("rpm payload format %q is not cpio", h.PayloadFormat)
	}

	var payload io.Reader
	switch h.PayloadCompressor {
	case "zstd":
		dec, err := zstd.NewReader(br)
		if err != nil {
			return nil, h, fmt.Errorf("rpm zstd payload: %w", err)
		}
		payload = dec
	case "gzip", "":
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, h, fmt.Errorf("rpm gzip payload: %w", err)
		}
		payload = gz
	default:
		return nil, h, fmt.Errorf("rpm payload compressor %q is not supported (only zstd and gzip)", h.PayloadCompressor)
	}
	return payload, h, nil
}

// readHeader parses one RPM header structure: the 16-byte preamble, the
// index of (tag, type, offset, count) entries and the data store. visit is
// called with each entry's raw store bytes from its offset to the next
// entry's offset (entries sorted by offset), which is enough for strings.
func readHeader(r io.Reader, visit func(tag, typ int32, value []byte)) (nindex, hsize int, err error) {
	pre := make([]byte, 16)
	if _, err := io.ReadFull(r, pre); err != nil {
		return 0, 0, err
	}
	if string(pre[:4]) != headerMagic {
		return 0, 0, fmt.Errorf("bad header magic %x", pre[:4])
	}
	nindex = int(binary.BigEndian.Uint32(pre[8:12]))
	hsize = int(binary.BigEndian.Uint32(pre[12:16]))
	if nindex < 0 || hsize < 0 || nindex*16 > maxHeaderBytes || hsize > maxHeaderBytes {
		return 0, 0, fmt.Errorf("header too large: %d entries, %d bytes", nindex, hsize)
	}
	index := make([]byte, nindex*16)
	if _, err := io.ReadFull(r, index); err != nil {
		return 0, 0, fmt.Errorf("index: %w", err)
	}
	store := make([]byte, hsize)
	if _, err := io.ReadFull(r, store); err != nil {
		return 0, 0, fmt.Errorf("store: %w", err)
	}
	if visit == nil {
		return nindex, hsize, nil
	}

	type entry struct {
		tag, typ int32
		offset   int
	}
	entries := make([]entry, 0, nindex)
	for i := 0; i < nindex; i++ {
		e := index[i*16 : i*16+16]
		off := int(binary.BigEndian.Uint32(e[8:12]))
		if off < 0 || off > hsize {
			return 0, 0, fmt.Errorf("entry %d offset %d outside store of %d bytes", i, off, hsize)
		}
		entries = append(entries, entry{tag: int32(binary.BigEndian.Uint32(e[0:4])), typ: int32(binary.BigEndian.Uint32(e[4:8])), offset: off})
	}
	for _, e := range entries {
		end := hsize
		for _, other := range entries {
			if other.offset > e.offset && other.offset < end {
				end = other.offset
			}
		}
		visit(e.tag, e.typ, store[e.offset:end])
	}
	return nindex, hsize, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ErrNotFound is returned by Extract when a requested member is missing.
var ErrNotFound = errors.New("member not found in rpm payload")

// Extract reads the RPM on r and returns the contents of the requested
// members, keyed as requested. Names are matched after stripping the
// leading "./" or "/" cpio archives carry, so "usr/lib/efi/x.efi" finds
// "./usr/lib/efi/x.efi"; a name may use path.Match wildcards for one path
// element ("usr/lib/efi/grub2/*/EFI/fedora/grubx64.efi" skips the
// epoch-version directory). Every requested member must match exactly one
// regular file.
func Extract(r io.Reader, names ...string) (map[string][]byte, error) {
	payload, _, err := Payload(r)
	if err != nil {
		return nil, err
	}
	want := make([]string, len(names))
	for i, n := range names {
		want[i] = normalizeName(n)
	}
	out := make(map[string][]byte, len(names))
	cr := NewCpioReader(payload)
	for {
		hdr, err := cr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !hdr.Mode.IsRegular() {
			continue
		}
		name := normalizeName(hdr.Name)
		var data []byte
		for i, pattern := range want {
			if matched, _ := path.Match(pattern, name); !matched {
				continue
			}
			if _, dup := out[names[i]]; dup {
				return nil, fmt.Errorf("%s matches more than one member", names[i])
			}
			if data == nil {
				if data, err = io.ReadAll(cr); err != nil {
					return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
				}
			}
			out[names[i]] = data
		}
	}
	var missing []string
	for _, n := range names {
		if _, ok := out[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, strings.Join(missing, ", "))
	}
	return out, nil
}

func normalizeName(n string) string {
	n = strings.TrimPrefix(n, "./")
	return strings.TrimPrefix(n, "/")
}
