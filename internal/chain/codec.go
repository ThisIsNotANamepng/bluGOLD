package chain

import (
	"bytes"
	"encoding/binary"
)

func writeU64(buf *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	buf.Write(b[:])
}

func writeI64(buf *bytes.Buffer, v int64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(v))
	buf.Write(b[:])
}

func writeString(buf *bytes.Buffer, s string) {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(s)))
	buf.Write(l[:])
	buf.WriteString(s)
}

func writeBytes(buf *bytes.Buffer, data []byte) {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(data)))
	buf.Write(l[:])
	buf.Write(data)
}
