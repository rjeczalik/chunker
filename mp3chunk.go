// Package mp3chunk splits audio streams into self-contained chunks.
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

// Chunker interface for different audio file types
type Chunker interface {
	Next() ([]byte, error)
}

// MP3Chunker yields MP3 chunks suitable for HTTP streaming.
// Each chunk starts with a valid frame boundary and includes previous data for bit reservoir.
type MP3Chunker struct {
	r            io.Reader
	targetSize   int
	buf          []byte
	err          error
	reservoir    []byte // bit reservoir data from previous chunks
	reservoirCap int
}

// NewMP3Chunker returns a new MP3Chunker that reads from r.
func NewMP3Chunker(r io.Reader, chunkSize, reservoirSize int) *MP3Chunker {
	if reservoirSize > maxReservoir {
		reservoirSize = maxReservoir
	}
	return &MP3Chunker{
		r:            r,
		targetSize:   chunkSize,
		buf:          make([]byte, 4),
		reservoirCap: reservoirSize,
	}
}

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

// findNextFrame finds the next valid MP3 frame header in the stream
func (c *MP3Chunker) findNextFrame() ([]byte, error) {
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
func (c *MP3Chunker) Next() ([]byte, error) {
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
func (c *MP3Chunker) finalize(chunk []byte) []byte {
	if len(chunk) > c.reservoirCap {
		c.reservoir = append([]byte(nil), chunk[len(chunk)-c.reservoirCap:]...)
	} else {
		c.reservoir = append([]byte(nil), chunk...)
	}
	return chunk
}

// WAVChunker yields WAV chunks suitable for HTTP streaming.
// WAV files are much simpler to chunk since they don't have frame dependencies.
type WAVChunker struct {
	r          io.Reader
	targetSize int
	err        error
	header     []byte
	headerSent bool
	dataStart  int64
	bytesRead  int64
	dataSize   uint32
}

// NewWAVChunker returns a new WAVChunker that reads from r.
func NewWAVChunker(r io.Reader, chunkSize int) *WAVChunker {
	return &WAVChunker{
		r:          r,
		targetSize: chunkSize,
	}
}

// readUint32LE reads a 32-bit little-endian unsigned integer
func readUint32LE(data []byte) uint32 {
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
}

// readUint16LE reads a 16-bit little-endian unsigned integer
func readUint16LE(data []byte) uint16 {
	return uint16(data[0]) | uint16(data[1])<<8
}

// parseWAVHeader parses the WAV header according to Resemble.AI specification
func (c *WAVChunker) parseWAVHeader() error {
	var headerBuffer []byte

	// Read RIFF header (12 bytes)
	riffHeader := make([]byte, 12)
	n, err := io.ReadFull(c.r, riffHeader)
	if err != nil {
		return err
	}
	if n != 12 {
		return errors.New("incomplete RIFF header")
	}

	// Check RIFF signature
	if string(riffHeader[0:4]) != "RIFF" {
		return errors.New("not a valid WAV file: missing RIFF signature")
	}

	// Check WAVE signature
	if string(riffHeader[8:12]) != "WAVE" {
		return errors.New("not a valid WAV file: missing WAVE signature")
	}

	headerBuffer = append(headerBuffer, riffHeader...)

	// Read chunks until we find the data chunk
	for {
		chunkHeader := make([]byte, 8)
		n, err := io.ReadFull(c.r, chunkHeader)
		if err != nil {
			return err
		}
		if n != 8 {
			return errors.New("incomplete chunk header")
		}

		chunkID := string(chunkHeader[0:4])
		chunkSize := readUint32LE(chunkHeader[4:8])

		headerBuffer = append(headerBuffer, chunkHeader...)

		if chunkID == "data" {
			// Found the data chunk
			c.dataSize = chunkSize
			c.header = headerBuffer
			c.dataStart = int64(len(headerBuffer))
			c.bytesRead = int64(len(headerBuffer))
			return nil
		}

		// Read and include the chunk data in the header
		chunkData := make([]byte, chunkSize)
		n, err = io.ReadFull(c.r, chunkData)
		if err != nil {
			return err
		}
		if n != int(chunkSize) {
			return errors.New("incomplete chunk data")
		}

		headerBuffer = append(headerBuffer, chunkData...)

		// WAV chunks must be aligned on 2-byte boundaries
		if chunkSize%2 == 1 {
			padding := make([]byte, 1)
			n, err = io.ReadFull(c.r, padding)
			if err != nil && err != io.EOF {
				return err
			}
			if n == 1 {
				headerBuffer = append(headerBuffer, padding...)
			}
		}
	}
}

// Next returns the next chunk or io.EOF when done.
func (c *WAVChunker) Next() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}

	// Parse header on first call
	if !c.headerSent {
		if err := c.parseWAVHeader(); err != nil {
			c.err = err
			return nil, err
		}
		c.headerSent = true

		// For the first chunk, include the header
		chunk := make([]byte, len(c.header))
		copy(chunk, c.header)

		// Add audio data to fill up to target size
		remaining := c.targetSize - len(chunk)
		if remaining > 0 {
			// Don't read more than the actual data size
			audioDataLeft := int64(c.dataSize) - (c.bytesRead - c.dataStart)
			if audioDataLeft <= 0 {
				return chunk, nil
			}

			readSize := remaining
			if int64(readSize) > audioDataLeft {
				readSize = int(audioDataLeft)
			}

			audioData := make([]byte, readSize)
			n, err := c.r.Read(audioData)
			if err != nil && err != io.EOF {
				c.err = err
				return nil, err
			}
			if n > 0 {
				chunk = append(chunk, audioData[:n]...)
				c.bytesRead += int64(n)
			}
		}

		return chunk, nil
	}

	// Check if we've read all the audio data
	audioDataLeft := int64(c.dataSize) - (c.bytesRead - c.dataStart)
	if audioDataLeft <= 0 {
		return nil, io.EOF
	}

	// For subsequent chunks, just read audio data
	readSize := c.targetSize
	if int64(readSize) > audioDataLeft {
		readSize = int(audioDataLeft)
	}

	chunk := make([]byte, readSize)
	n, err := c.r.Read(chunk)
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		c.err = err
		return nil, err
	}

	c.bytesRead += int64(n)
	return chunk[:n], nil
}

// DumbChunker splits any file into fixed-size chunks without parsing
type DumbChunker struct {
	r          io.Reader
	targetSize int
	err        error
}

// NewDumbChunker returns a new DumbChunker that reads from r.
func NewDumbChunker(r io.Reader, chunkSize int) *DumbChunker {
	return &DumbChunker{
		r:          r,
		targetSize: chunkSize,
	}
}

// Next returns the next chunk or io.EOF when done.
func (c *DumbChunker) Next() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}

	chunk := make([]byte, c.targetSize)
	n, err := c.r.Read(chunk)
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		c.err = err
		return nil, err
	}

	return chunk[:n], nil
}
