// Package mp3chunk splits an MP3 stream into self-contained chunks.
package main

import (
	"errors"
	"io"
)

const (
	maxReservoir = 511  // largest possible bit-reservoir (ISO spec)
	minFrameSize = 24   // smallest valid frame header + side-info
	maxFrameSize = 1732 // maximum frame size for MPEG-1 Layer I at 320kbps
)

// ErrInvalidFrame is returned when the bit-stream does not contain a valid MP3 frame.
var ErrInvalidFrame = errors.New("invalid or unsupported MP3 frame")

// frameLength returns the length in bytes of the frame described by hdr.
// hdr must be exactly four bytes.
func frameLength(hdr []byte) (int, error) {
	if len(hdr) != 4 {
		return 0, ErrInvalidFrame
	}

	// Check frame sync - must be 0xFF followed by at least 0xE0
	if hdr[0] != 0xff || hdr[1]&0xe0 != 0xe0 {
		return 0, ErrInvalidFrame
	}

	// MPEG version and layer
	mpegVer := (hdr[1] >> 3) & 0x03
	layer := (hdr[1] >> 1) & 0x03
	if mpegVer == 1 || layer != 1 { // mpegVer == 1 is reserved, layer != 1 means not Layer III
		return 0, ErrInvalidFrame
	}

	// Check other reserved/invalid values
	bitRateIdx := (hdr[2] >> 4) & 0x0f
	if bitRateIdx == 0 || bitRateIdx == 0x0f {
		return 0, ErrInvalidFrame
	}
	sampleRateIdx := (hdr[2] >> 2) & 0x03
	if sampleRateIdx == 3 {
		return 0, ErrInvalidFrame
	}
	emphasis := hdr[3] & 0x03
	if emphasis == 2 {
		return 0, ErrInvalidFrame
	}

	// Determine bitrate and sample rate tables based on MPEG version
	var bitrates []int
	var sampleRates []int
	var multiplier int

	if mpegVer == 3 { // MPEG-1
		bitrates = []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
		sampleRates = []int{44100, 48000, 32000, 0}
		multiplier = 144
	} else { // MPEG-2 and MPEG-2.5
		bitrates = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
		sampleRates = []int{22050, 24000, 16000, 0}
		if mpegVer == 0 { // MPEG-2.5
			sampleRates = []int{11025, 12000, 8000, 0}
		}
		multiplier = 72
	}

	padding := int((hdr[2] >> 1) & 1)
	bitRate := bitrates[bitRateIdx] * 1000
	sampleRate := sampleRates[sampleRateIdx]

	if bitRate == 0 || sampleRate == 0 {
		return 0, ErrInvalidFrame
	}

	return multiplier*bitRate/sampleRate + padding, nil
}

// Chunker yields MP3 chunks suitable for HTTP streaming.
// Each chunk starts with a valid frame boundary and includes previous data for bit reservoir.
type Chunker struct {
	r            io.Reader
	targetSize   int
	buf          []byte
	err          error
	reservoir    []byte // bit reservoir data from previous chunks
	reservoirCap int
}

// NewChunker returns a new Chunker that reads from r.
func NewChunker(r io.Reader, chunkSize, reservoirSize int) *Chunker {
	if reservoirSize > maxReservoir {
		reservoirSize = maxReservoir
	}
	return &Chunker{
		r:            r,
		targetSize:   chunkSize,
		buf:          make([]byte, 4),
		reservoirCap: reservoirSize,
	}
}

// findNextFrame finds the next valid MP3 frame header in the stream
func (c *Chunker) findNextFrame() ([]byte, error) {
	for {
		// Read one byte at a time looking for sync
		n, err := io.ReadFull(c.r, c.buf[:1])
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, io.ErrUnexpectedEOF
		}

		// Check for sync byte
		if c.buf[0] != 0xff {
			continue
		}

		// Read the next 3 bytes to complete potential header
		n, err = io.ReadFull(c.r, c.buf[1:4])
		if err != nil {
			return nil, err
		}
		if n != 3 {
			return nil, io.ErrUnexpectedEOF
		}

		// Check if this is a valid frame header
		if c.buf[1]&0xe0 == 0xe0 {
			if _, err := frameLength(c.buf[:4]); err == nil {
				return append([]byte(nil), c.buf[:4]...), nil
			}
		}

		// False sync - shift buffer and try again
		c.buf[0] = c.buf[1]
		c.buf[1] = c.buf[2]
		c.buf[2] = c.buf[3]
	}
}

// Next returns the next chunk or io.EOF when done.
func (c *Chunker) Next() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}

	// Start chunk with reservoir data
	chunk := append([]byte(nil), c.reservoir...)
	remaining := c.targetSize - len(chunk)

	// Read frames until we have enough data
	for remaining > 0 {
		// Find next frame header
		hdr, err := c.findNextFrame()
		if err != nil {
			c.err = err
			if len(chunk) > len(c.reservoir) {
				return c.finalize(chunk), nil
			}
			return nil, err
		}

		// Get frame length
		frameLen, err := frameLength(hdr)
		if err != nil {
			c.err = err
			return nil, err
		}

		// Read the rest of the frame
		frame := make([]byte, frameLen)
		copy(frame, hdr)
		if _, err := io.ReadFull(c.r, frame[4:]); err != nil {
			c.err = err
			if len(chunk) > len(c.reservoir) {
				return c.finalize(chunk), nil
			}
			return nil, err
		}

		// Add frame to chunk
		chunk = append(chunk, frame...)
		remaining -= len(frame)
	}

	return c.finalize(chunk), nil
}

// finalize trims the reservoir for the next iteration.
func (c *Chunker) finalize(chunk []byte) []byte {
	if len(chunk) > c.reservoirCap {
		c.reservoir = append([]byte(nil), chunk[len(chunk)-c.reservoirCap:]...)
	} else {
		c.reservoir = append([]byte(nil), chunk...)
	}
	return chunk
}
