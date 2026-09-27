package rpm

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
)

const (
	cpioNewcMagic = "070701"
	cpioCrcMagic  = "070702"
	cpioHeaderLen = 110
	cpioTrailer   = "TRAILER!!!"
)

// CpioHeader describes one member of a "newc" (SVR4) cpio archive, the
// format rpm payloads use.
type CpioHeader struct {
	Name string
	Mode fs.FileMode
	Size int64
}

// CpioReader iterates over a newc cpio archive; after Next it reads the
// current member's data and stops at its end.
type CpioReader struct {
	r         io.Reader
	remaining int64
	pad       int64
	err       error
}

// NewCpioReader wraps r, which must be positioned at the first header.
func NewCpioReader(r io.Reader) *CpioReader {
	return &CpioReader{r: r}
}

// Next skips the rest of the current member and returns the next header,
// or io.EOF at the TRAILER!!! entry.
func (c *CpioReader) Next() (*CpioHeader, error) {
	if c.err != nil {
		return nil, c.err
	}
	if _, err := io.CopyN(io.Discard, c.r, c.remaining+c.pad); err != nil {
		return nil, c.fail(fmt.Errorf("cpio: skipping member: %w", err))
	}
	c.remaining, c.pad = 0, 0

	hdr := make([]byte, cpioHeaderLen)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		return nil, c.fail(fmt.Errorf("cpio: header: %w", err))
	}
	magic := string(hdr[:6])
	if magic != cpioNewcMagic && magic != cpioCrcMagic {
		return nil, c.fail(fmt.Errorf("cpio: bad magic %q", magic))
	}
	field := func(i int) (int64, error) {
		v, err := strconv.ParseUint(string(hdr[6+i*8:14+i*8]), 16, 32)
		return int64(v), err
	}
	mode, err := field(1)
	if err != nil {
		return nil, c.fail(fmt.Errorf("cpio: mode: %w", err))
	}
	size, err := field(6)
	if err != nil {
		return nil, c.fail(fmt.Errorf("cpio: filesize: %w", err))
	}
	nameSize, err := field(11)
	if err != nil {
		return nil, c.fail(fmt.Errorf("cpio: namesize: %w", err))
	}
	if nameSize == 0 || nameSize > 4096 {
		return nil, c.fail(fmt.Errorf("cpio: namesize %d out of range", nameSize))
	}
	namePad := pad4(cpioHeaderLen + nameSize)
	name := make([]byte, nameSize+namePad)
	if _, err := io.ReadFull(c.r, name); err != nil {
		return nil, c.fail(fmt.Errorf("cpio: name: %w", err))
	}
	if name[nameSize-1] != 0 {
		return nil, c.fail(errors.New("cpio: name not NUL-terminated"))
	}
	h := &CpioHeader{Name: string(name[:nameSize-1]), Mode: unixMode(uint32(mode)), Size: size}
	if h.Name == cpioTrailer {
		return nil, c.fail(io.EOF)
	}
	c.remaining = size
	c.pad = pad4(size)
	return h, nil
}

// Read returns the current member's data, io.EOF at its end.
func (c *CpioReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if err == io.EOF && c.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err != nil && err != io.EOF {
		return n, c.fail(err)
	}
	if c.remaining == 0 {
		return n, io.EOF
	}
	return n, nil
}

func (c *CpioReader) fail(err error) error {
	c.err = err
	return err
}

func pad4(n int64) int64 {
	return (4 - n%4) % 4
}

// unixMode converts the cpio st_mode field to fs.FileMode: permission
// bits plus the type from the high nibble (0100000 regular, 040000 dir,
// 0120000 symlink).
func unixMode(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	switch m & 0o170000 {
	case 0o040000:
		mode |= fs.ModeDir
	case 0o120000:
		mode |= fs.ModeSymlink
	case 0o100000:
	default:
		mode |= fs.ModeIrregular
	}
	return mode
}
