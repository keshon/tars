package tools

import (
	"bufio"
	"encoding/binary"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// utf16Reader decodes incrementally; a huge UTF-16 file needs no full copy.
type utf16Reader struct {
	reader  *bufio.Reader
	little  bool
	pending []byte
	encoded [utf8.UTFMax]byte
	unit    uint16
	hasUnit bool
}

func (r *utf16Reader) next() (uint16, error) {
	if r.hasUnit {
		r.hasUnit = false
		return r.unit, nil
	}
	var data [2]byte
	if _, err := io.ReadFull(r.reader, data[:]); err != nil {
		return 0, err
	}
	if r.little {
		return binary.LittleEndian.Uint16(data[:]), nil
	}
	return binary.BigEndian.Uint16(data[:]), nil
}
func (r *utf16Reader) Read(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		if len(r.pending) == 0 {
			unit, err := r.next()
			if err != nil {
				if err == io.EOF && written > 0 {
					return written, nil
				}
				return written, err
			}
			decoded := rune(unit)
			if unit >= 0xd800 && unit <= 0xdbff {
				low, err := r.next()
				if err == io.EOF {
					decoded = utf8.RuneError
				} else if err != nil {
					return written, err
				} else if low >= 0xdc00 && low <= 0xdfff {
					decoded = utf16.DecodeRune(rune(unit), rune(low))
				} else {
					decoded = utf8.RuneError
					r.unit = low
					r.hasUnit = true
				}
			} else if unit >= 0xdc00 && unit <= 0xdfff {
				decoded = utf8.RuneError
			}
			r.pending = utf8.AppendRune(r.encoded[:0], decoded)
		}
		n := copy(p[written:], r.pending)
		r.pending = r.pending[n:]
		written += n
	}
	return written, nil
}
