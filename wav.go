package main

import (
	"errors"
	"io"
	"runtime"
	"sync"
)

const defaultChunkSize = 8192

// Maximum size for non-data chunks to prevent OOM attacks
const maxChunkSize = 1024 * 1024 // 1MB should be more than enough for WAV metadata
const maxHeaderSize = 8 << 20    // 8 MB
const minChunkSize = 1024        // 1KB

// Pool for reusable byte buffers with 512 capacity
// Beneficial for concurrent operations in service environments
var headerBufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 0, 512)
	},
}

// Pool for reusable audio buffers with 8192 capacity
// Allocated once per chunker instance, reused across all chunks
var audioBufferPool = sync.Pool{
	New: func() interface{} {
		// the chunks can be enlarged by the WAVChunker.Next(),
		// but they will eventually align with resemble chunk sizes
		return make([]byte, defaultChunkSize)
	},
}

// Helper function to compare 4 bytes to a string
func compareID(data []byte, id string) bool {
	if len(data) < 4 || len(id) != 4 {
		return false
	}
	return data[0] == id[0] && data[1] == id[1] && data[2] == id[2] && data[3] == id[3]
}

// WAVChunker yields WAV chunks as complete WAV files.
// WAV files are much simpler to chunk since they don't have frame dependencies.
type WAVChunker struct {
	r              io.Reader
	targetSize     int
	err            error
	headerSent     bool
	dataStart      int64
	bytesRead      int64
	dataSize       uint32
	dataSizeOffset int64
	closed         bool
	// Reusable buffers to reduce allocations
	riff    []byte
	chunk   []byte
	header  []byte
	audio   []byte
	padding [1]byte
}

// NewWAVChunker returns a new WAVChunker that reads from r with fixed 8192 chunk size.
func NewWAVChunker(r io.Reader) *WAVChunker {
	c := &WAVChunker{
		r:          r,
		targetSize: defaultChunkSize,
		riff:       make([]byte, 12),                // Reusable RIFF header buffer
		chunk:      make([]byte, 8),                 // Reusable 8-byte buffer for chunk headers
		header:     headerBufferPool.Get().([]byte), // Reusable header buffer
		audio:      audioBufferPool.Get().([]byte),  // Get audio buffer from pool
	}
	// Set finalizer to ensure pool cleanup even if client abandons iteration
	runtime.SetFinalizer(c, (*WAVChunker).Close)
	return c
}

// reset returns the buffers back to their respective pools
func (c *WAVChunker) reset() {
	if c.closed {
		return
	}
	c.closed = true

	c.resetAudioBuffer()
	c.resetHeaderBuffer()

	// Clear finalizer since we're explicitly closing
	runtime.SetFinalizer(c, nil)
}

func (c *WAVChunker) resetAudioBuffer() {
	if c.audio != nil {
		audioBufferPool.Put(c.audio)
		c.audio = nil
	}
}

func (c *WAVChunker) resetHeaderBuffer() {
	if c.header != nil {
		headerBufferPool.Put(c.header)
		c.header = nil
	}
}

// Close returns the buffers back to their respective pools and clears the finalizer.
// Safe to call multiple times.
func (c *WAVChunker) Close() {
	c.reset()
}

// writeUint32LE writes a 32-bit little-endian unsigned integer
func writeUint32LE(value uint32) []byte {
	return []byte{
		byte(value),
		byte(value >> 8),
		byte(value >> 16),
		byte(value >> 24),
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
	// Reset header buffer to ensure no leftover data from pool
	c.header = c.header[:0]

	// Read RIFF header (12 bytes) - reuse buffer
	n, err := io.ReadFull(c.r, c.riff)
	if err != nil {
		return err
	}
	if n != 12 {
		return errors.New("incomplete RIFF header")
	}

	// Check RIFF signature using byte comparison
	if !compareID(c.riff[0:4], "RIFF") {
		return errors.New("not a valid WAV file: missing RIFF signature")
	}

	// Check WAVE signature using byte comparison
	if !compareID(c.riff[8:12], "WAVE") {
		return errors.New("not a valid WAV file: missing WAVE signature")
	}

	c.header = append(c.header, c.riff...)

	// Read chunks until we find the data chunk
	for {
		if len(c.header) > maxHeaderSize {
			return errors.New("wav header too large")
		}
		// Reuse the chunk buffer
		n, err := io.ReadFull(c.r, c.chunk)
		if err != nil {
			return err
		}
		if n != 8 {
			return errors.New("incomplete chunk header")
		}

		// Use byte comparison instead of string conversion
		isDataChunk := compareID(c.chunk[0:4], "data")
		chunkSize := readUint32LE(c.chunk[4:8])

		c.header = append(c.header, c.chunk...)

		if isDataChunk {
			// Found the data chunk
			c.dataSize = chunkSize
			c.dataStart = int64(len(c.header))
			c.dataSizeOffset = int64(len(c.header) - 4)
			c.bytesRead = int64(len(c.header))
			return nil
		}

		// Read and include the chunk data in the header
		// Guard against maliciously large chunk sizes that could cause OOM
		if chunkSize > maxChunkSize {
			return errors.New("chunk size too large")
		}

		chunkData := make([]byte, chunkSize)
		n, err = io.ReadFull(c.r, chunkData)
		if err != nil {
			return err
		}
		if n != int(chunkSize) {
			return errors.New("incomplete chunk data")
		}

		c.header = append(c.header, chunkData...)

		// WAV chunks must be aligned on 2-byte boundaries
		if chunkSize%2 == 1 {
			n, err = io.ReadFull(c.r, c.padding[:])
			if isErrNotEOF(err) {
				return err
			}
			if n == 1 {
				c.header = append(c.header, c.padding[:]...)
			}
		}
	}
}

// createCompleteWAVFile creates a complete WAV file from header and audio data
// Returns nil when audioData is empty
func (c *WAVChunker) createCompleteWAVFile(audioData []byte) []byte {
	if len(audioData) == 0 {
		return nil
	}

	headerLen := len(c.header)
	audioLen := len(audioData)
	totalLen := headerLen + audioLen

	// Allocate result buffer once
	result := make([]byte, totalLen)

	// Copy header
	copy(result, c.header)

	// Update the data chunk size (last 4 bytes of header)
	dataSize := writeUint32LE(uint32(audioLen))
	copy(result[c.dataSizeOffset:c.dataSizeOffset+4], dataSize)

	// Update the overall file size in RIFF header (at offset 4)
	totalSize := totalLen - 8 // -8 for RIFF header itself
	riffSize := writeUint32LE(uint32(totalSize))
	copy(result[4:8], riffSize)

	// Copy audio data
	copy(result[headerLen:], audioData)

	return result
}

// Next returns the next chunk or io.EOF when done.
func (c *WAVChunker) Next() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}

	// Parse header on first call
	if !c.headerSent {
		if err := c.parseWAVHeader(); err != nil {
			c.reset()
			c.err = err
			return nil, err
		}
		c.headerSent = true
	}

	// Check if we've read all the audio data
	audioDataLeft := int64(c.dataSize) - (c.bytesRead - c.dataStart)
	if audioDataLeft <= 0 {
		// If data size is odd, consume the padding byte
		if c.dataSize%2 == 1 {
			_, err := io.ReadFull(c.r, c.padding[:])
			if isErrNotEOF(err) {
				c.reset()
				c.err = err
				return nil, err
			}
		}
		c.reset()
		return nil, io.EOF
	}

	// Read audio data for this chunk
	// Subtract header size from target to leave room for header
	readSize := c.targetSize - len(c.header)
	if readSize <= 0 {
		readSize = minChunkSize
	}

	if int64(readSize) > audioDataLeft {
		readSize = int(audioDataLeft)
	}

	// Resize audio buffer if needed
	if len(c.audio) < readSize {
		if cap(c.audio) >= readSize {
			// We have enough capacity, just extend the slice
			c.audio = c.audio[:readSize]
		} else {
			c.resetAudioBuffer()
			// Allocate new buffer (can't use pool for sizes > defaultChunkSize)
			c.audio = make([]byte, readSize)
		}
	}

	// Read directly into the reusable buffer using ReadFull to avoid partial reads
	n, err := io.ReadFull(c.r, c.audio[:readSize])
	if err != nil && !errors.Is(err, io.EOF) {
		c.reset()
		c.err = err
		return nil, err
	}

	c.bytesRead += int64(n)

	audioData := c.audio[:n] // Slice the buffer to actual read size
	// Each chunk is a complete WAV file
	chunk := c.createCompleteWAVFile(audioData)

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// We stop processing when we hit EOF or unexpected EOF
		// Both are treated as end of stream - return chunk with nil error
		c.reset()
		c.err = io.EOF
		// Don't return the error - next call will return io.EOF naturally
		return chunk, nil
	}

	return chunk, nil
}

func isErrNotEOF(err error) bool {
	return err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)
}
