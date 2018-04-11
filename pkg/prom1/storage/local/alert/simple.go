// This file was taken from Prometheus (https://github.com/prometheus/prometheus).
// The original license header is included below:
//
// Copyright 2014 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package alert

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"

	"github.com/prometheus/common/model"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
)

// The 13-byte header of a simple-encoded chunk looks like:
//
// - time delta bytes:  1 bytes
// - value delta bytes: 1 bytes
// - is integer:        1 byte
// - base time:         8 bytes
// - base value:        8 bytes
// - used buf bytes:    2 bytes

// each chunk has <header>:<time><value><time><value>
// where <header> = <timebytes(representing how many bytes does the time value occupy)><valuebytes (how many bytes for value)>
// - time bytes: 	1 bytes  - value 8
// - value bytes:	2 bytes	- value 1024 (need to increase this for bigger alert texts)
const (
	simpleHeaderBytes = 21

	simpleHeaderTimeBytesOffset  = 0
	simpleHeaderValueBytesOffset = 1
	simpleHeaderBaseTimeOffset   = 3
	simpleHeaderBufLenOffset     = 11 // + 8 the filled buffer length (8 bytes uint64) XXX:Moteesh find out the appropriate length for alert text
)

type deltaBytes byte

const (
	d0 deltaBytes = 0
	d1 deltaBytes = 1
	d2 deltaBytes = 2
	d4 deltaBytes = 4
	d8 deltaBytes = 8
)

// A simpleEncodedChunk adaptively stores sample timestamps and values with a
// timestamps are saved directly as int64 and values as
// bytes. It implements the chunk interface.
type simpleEncodedChunk []byte

// newSimpleEncodedChunk returns a newly allocated simpleEncodedChunk.
func newSimpleEncodedChunk(tb deltaBytes, vb int, isInt bool, length int) *simpleEncodedChunk {
	if tb < 1 {
		panic("need at least 1 time delta byte")
	}
	if length < simpleHeaderBytes+ValueLen {
		panic(fmt.Errorf(
			"chunk length %d bytes is insufficient, need at least %d",
			length, simpleHeaderBytes+ValueLen,
		))
	}
	c := make(simpleEncodedChunk, simpleHeaderBufLenOffset, length)

	c[simpleHeaderTimeBytesOffset] = byte(tb)
	binary.LittleEndian.PutUint16(c[simpleHeaderValueBytesOffset:], uint16(vb))

	return &c
}

// Add implements chunk.
func (c simpleEncodedChunk) Add(s client.MessageTracker) ([]Chunk, error) {
	log.Printf("inside simpleEncodedChunk Add method c.Len = %v", c.Len())
	// TODO(beorn7): Since we return &c, this method might cause an unnecessary allocation.
	if c.Len() == 0 {
		c = c[:simpleHeaderBytes]
		log.Printf("inside c.Len() == 0 c.Len() = %v", c.Len())
		binary.LittleEndian.PutUint64(c[simpleHeaderBaseTimeOffset:], uint64(s.Timestamp))
		// //TODO: Moteesh - how many bytes does the alert value contain ??
		// // binary.LittleEndian.PutUint64(c[deltaHeaderBaseValueOffset:], math.Float64bits(float64(s.Value)))

		// copy(c[deltaHeaderBaseValueOffset:], []byte(s.Value))

	}
	log.Printf("value length =%v, bytes = %v", len(s.Value), len([]byte(s.Value)))
	remainingBytes := cap(c) - len(c)
	sampleSize := c.sampleSize()
	log.Printf("remainingBytes = %v, sampleSize = %v", remainingBytes, sampleSize)
	// Do we generally have space for another sample in this chunk? If not,
	// overflow into a new one.
	if remainingBytes < sampleSize {
		log.Print("addToOverflowChunk")
		return addToOverflowChunk(&c, s)
	}

	// baseValue := c.baseValue()
	// dt := model.Time(s.Timestamp) - c.baseTime()
	// if dt < 0 {
	// 	return nil, fmt.Errorf("time delta is less than zero: %v", dt)
	// }

	// dv := s.Value - baseValue
	tb := c.timeBytes()
	// vb := c.valueBytes()
	// isInt := c.isInt()

	// If the new sample is incompatible with the current encoding, reencode the
	// existing chunk data into new chunk(s).

	// ntb, nvb, nInt := tb, vb, isInt
	// if isInt && !isInt64(dv) {
	// 	// int->float.
	// 	nvb = d4
	// 	nInt = false
	// } else if !isInt && vb == d4 && baseValue+model.SampleValue(float32(dv)) != s.Value {
	// 	// float32->float64.
	// 	nvb = d8
	// } else {
	// 	if tb < d8 {
	// 		// Maybe more bytes for timestamp.
	// 		ntb = max(tb, bytesNeededForUnsignedTimestampDelta(dt))
	// 	}
	// 	if c.isInt() && vb < d8 {
	// 		// Maybe more bytes for sample value.
	// 		nvb = max(vb, bytesNeededForIntegerSampleValueDelta(dv))
	// 	}
	// }
	// nvb = len([]byte(s.Value))
	// if tb < d8 {
	// 	// Maybe more bytes for timestamp.
	// 	ntb = max(tb, bytesNeededForUnsignedTimestampDelta(dt))
	// }
	// if tb != ntb || vb != nvb || isInt != nInt {
	// 	if len(c)*2 < cap(c) {
	// 		return transcodeAndAdd(newSimpleEncodedChunk(ntb, nvb, nInt, cap(c)), &c, s)
	// 	}
	// 	// Chunk is already half full. Better create a new one and save the transcoding efforts.
	// 	return addToOverflowChunk(&c, s)
	// }

	offset := len(c)
	log.Printf("offset = len(c) = %v, tb = %v", offset, tb)
	c = c[:offset+sampleSize]

	// switch tb {
	// case d1:
	// 	c[offset] = byte(dt)
	// case d2:
	// 	binary.LittleEndian.PutUint16(c[offset:], uint16(dt))
	// case d4:
	// 	binary.LittleEndian.PutUint32(c[offset:], uint32(dt))
	// case d8:
	// 	// Store the absolute value (no delta) in case of d8.
	// 	binary.LittleEndian.PutUint64(c[offset:], uint64(s.Timestamp))
	// default:
	// 	return nil, fmt.Errorf("invalid number of bytes for time delta: %d", tb)
	// }

	binary.LittleEndian.PutUint64(c[offset:], uint64(s.Timestamp))
	offset += int(tb)

	// if c.isInt() {
	// 	switch vb {
	// 	case d0:
	// 		// No-op. Constant value is stored as base value.
	// 	case d1:
	// 		c[offset] = byte(int8(dv))
	// 	case d2:
	// 		binary.LittleEndian.PutUint16(c[offset:], uint16(int16(dv)))
	// 	case d4:
	// 		binary.LittleEndian.PutUint32(c[offset:], uint32(int32(dv)))
	// 	// d8 must not happen. Those samples are encoded as float64.
	// 	default:
	// 		return nil, fmt.Errorf("invalid number of bytes for integer delta: %d", vb)
	// 	}
	// } else {
	// 	switch vb {
	// 	case d4:
	// 		binary.LittleEndian.PutUint32(c[offset:], math.Float32bits(float32(dv)))
	// 	case d8:
	// 		// Store the absolute value (no delta) in case of d8.
	// 		binary.LittleEndian.PutUint64(c[offset:], math.Float64bits(float64(s.Value)))
	// 	default:
	// 		return nil, fmt.Errorf("invalid number of bytes for floating point delta: %d", vb)
	// 	}
	// }
	log.Printf("simple encoding Add value : %s", s.Value)
	copy(c[offset:], []byte(s.Value))
	log.Printf("chunk value = %v", c)
	return []Chunk{&c}, nil
}

// Clone implements chunk.
func (c simpleEncodedChunk) Clone() Chunk {
	clone := make(simpleEncodedChunk, len(c), cap(c))
	copy(clone, c)
	return &clone
}

// FirstTime implements chunk.
func (c simpleEncodedChunk) FirstTime() model.Time {
	return c.baseTime()
}

// NewIterator implements chunk.
func (c *simpleEncodedChunk) NewIterator() Iterator {
	return newIndexAccessingChunkIterator(c.Len(), &simpleEncodedIndexAccessor{
		c:     *c,
		baseT: c.baseTime(),
		// baseV:  c.baseValue(),
		tBytes: c.timeBytes(),
		vBytes: c.valueBytes(),
		// isInt:  c.isInt(),
	})
}

// Marshal implements chunk.
func (c simpleEncodedChunk) Marshal(w io.Writer) error {
	if len(c) > math.MaxUint16 {
		panic("chunk buffer length would overflow a 16 bit uint.")
	}
	binary.LittleEndian.PutUint16(c[simpleHeaderBufLenOffset:], uint16(len(c)))

	n, err := w.Write(c[:cap(c)])
	if err != nil {
		return err
	}
	if n != cap(c) {
		return fmt.Errorf("wanted to write %d bytes, wrote %d", cap(c), n)
	}
	return nil
}

// MarshalToBuf implements chunk.
func (c simpleEncodedChunk) MarshalToBuf(buf []byte) error {
	if len(c) > math.MaxUint16 {
		panic("chunk buffer length would overflow a 16 bit uint")
	}
	binary.LittleEndian.PutUint16(c[simpleHeaderBufLenOffset:], uint16(len(c)))

	n := copy(buf, c)
	if n != len(c) {
		return fmt.Errorf("wanted to copy %d bytes to buffer, copied %d", len(c), n)
	}
	return nil
}

// Unmarshal implements chunk.
func (c *simpleEncodedChunk) Unmarshal(r io.Reader) error {
	*c = (*c)[:cap(*c)]
	if _, err := io.ReadFull(r, *c); err != nil {
		return err
	}
	return c.setLen()
}

// UnmarshalFromBuf implements chunk.
func (c *simpleEncodedChunk) UnmarshalFromBuf(buf []byte) error {
	*c = (*c)[:cap(*c)]
	copy(*c, buf)
	return c.setLen()
}

// setLen sets the length of the underlying slice and performs some sanity checks.
func (c *simpleEncodedChunk) setLen() error {
	l := binary.LittleEndian.Uint16((*c)[simpleHeaderBufLenOffset:])
	if int(l) > cap(*c) {
		return fmt.Errorf("delta chunk length exceeded during unmarshaling: %d", l)
	}
	if int(l) < simpleHeaderBytes {
		return fmt.Errorf("delta chunk length less than header size: %d < %d", l, simpleHeaderBytes)
	}
	switch c.timeBytes() {
	case d1, d2, d4, d8:
		// Pass.
	default:
		return fmt.Errorf("invalid number of time bytes in delta chunk: %d", c.timeBytes())
	}
	// switch c.valueBytes() {
	// case d0, d1, d2, d4, d8:
	// 	// Pass.
	// default:
	// 	return fmt.Errorf("invalid number of value bytes in delta chunk: %d", c.valueBytes())
	// }
	*c = (*c)[:l]
	return nil
}

// Encoding implements chunk.
func (c simpleEncodedChunk) Encoding() Encoding { return Delta }

// Utilization implements chunk.
func (c simpleEncodedChunk) Utilization() float64 {
	return float64(len(c)) / float64(cap(c))
}

func (c simpleEncodedChunk) timeBytes() deltaBytes {
	return deltaBytes(c[simpleHeaderTimeBytesOffset])
}

func (c simpleEncodedChunk) valueBytes() int {
	return int(binary.LittleEndian.Uint16(c[simpleHeaderValueBytesOffset:]))
}

// func (c simpleEncodedChunk) isInt() bool {
// 	return c[deltaHeaderIsIntOffset] == 1
// }

func (c simpleEncodedChunk) baseTime() model.Time {
	return model.Time(binary.LittleEndian.Uint64(c[simpleHeaderBaseTimeOffset:]))
}

// func (c simpleEncodedChunk) baseValue() []byte {
// 	return c[deltaHeaderBaseValueOffset:deltaHeaderBufLenOffset]
// }

func (c simpleEncodedChunk) sampleSize() int {
	return int(c.timeBytes()) + c.valueBytes()
}

// Len implements Chunk. Runs in constant time.
func (c simpleEncodedChunk) Len() int {
	if len(c) < simpleHeaderBytes {
		return 0
	}
	return (len(c) - simpleHeaderBytes) / c.sampleSize()
}

// simpleEncodedIndexAccessor implements indexAccessor.
type simpleEncodedIndexAccessor struct {
	c     simpleEncodedChunk
	baseT model.Time
	// baseV  []byte
	tBytes deltaBytes
	vBytes int
	// isInt          bool
	lastErr error
}

func (acc *simpleEncodedIndexAccessor) err() error {
	return acc.lastErr
}

func (acc *simpleEncodedIndexAccessor) timestampAtIndex(idx int) model.Time {
	offset := simpleHeaderBytes + idx*int(int(acc.tBytes)+acc.vBytes)

	switch acc.tBytes {
	case d1:
		return acc.baseT + model.Time(uint8(acc.c[offset]))
	case d2:
		return acc.baseT + model.Time(binary.LittleEndian.Uint16(acc.c[offset:]))
	case d4:
		return acc.baseT + model.Time(binary.LittleEndian.Uint32(acc.c[offset:]))
	case d8:
		// Take absolute value for d8.
		return model.Time(binary.LittleEndian.Uint64(acc.c[offset:]))
	default:
		acc.lastErr = fmt.Errorf("invalid number of bytes for time delta: %d", acc.tBytes)
		return model.Earliest
	}
}

func (acc *simpleEncodedIndexAccessor) sampleValueAtIndex(idx int) []byte {
	offset := simpleHeaderBytes + idx*int(int(acc.tBytes)+acc.vBytes) + int(acc.tBytes)

	return acc.c[offset : offset+acc.vBytes]
	// if acc.isInt {
	// 	switch acc.vBytes {
	// 	case d0:
	// 		return acc.baseV
	// 	case d1:
	// 		return acc.baseV + model.SampleValue(int8(acc.c[offset]))
	// 	case d2:
	// 		return acc.baseV + model.SampleValue(int16(binary.LittleEndian.Uint16(acc.c[offset:])))
	// 	case d4:
	// 		return acc.baseV + model.SampleValue(int32(binary.LittleEndian.Uint32(acc.c[offset:])))
	// 	// No d8 for ints.
	// 	default:
	// 		acc.lastErr = fmt.Errorf("invalid number of bytes for integer delta: %d", acc.vBytes)
	// 		return 0
	// 	}
	// } else {
	// 	switch acc.vBytes {
	// 	case d4:
	// 		return acc.baseV + model.SampleValue(math.Float32frombits(binary.LittleEndian.Uint32(acc.c[offset:])))
	// 	case d8:
	// 		// Take absolute value for d8.
	// 		return model.SampleValue(math.Float64frombits(binary.LittleEndian.Uint64(acc.c[offset:])))
	// 	default:
	// 		acc.lastErr = fmt.Errorf("invalid number of bytes for floating point delta: %d", acc.vBytes)
	// 		return 0
	// 	}
	// }
}
