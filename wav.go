package main

import (
	"errors"
	"io"
)

// Helper function to compare 4 bytes to a string
func compareID(data []byte, id string) bool {
	if len(data) < 4 || len(id) != 4 {
		return false
	}
	return data[0] == id[0] && data[1] == id[1] && data[2] == id[2] && data[3] == id[3]
}

// WAVChunkMode defines the chunking mode for WAV files
type WAVChunkMode int

const (
	// WAVModeStreaming - first chunk has header + data, subsequent chunks are raw audio (for HTTP streaming)
	WAVModeStreaming WAVChunkMode = iota
	// WAVModeComplete - each chunk is a complete WAV file (for local playback)
	WAVModeComplete
)

// WAVChunker yields WAV chunks suitable for HTTP streaming or complete playback.
// WAV files are much simpler to chunk since they don't have frame dependencies.
type WAVChunker struct {
	r          io.Reader
	targetSize int
	mode       WAVChunkMode
	err        error
	header     []byte
	headerSent bool
	dataStart  int64
	bytesRead  int64
	dataSize   uint32
	// Reusable buffers to reduce allocations
	chunkBuffer []byte
	audioBuffer []byte
	// Additional buffers for common operations
	paddingBuffer []byte
	riffBuffer    []byte
}

// NewWAVChunker returns a new WAVChunker that reads from r.
func NewWAVChunker(r io.Reader, chunkSize int, mode WAVChunkMode) *WAVChunker {
	return &WAVChunker{
		r:             r,
		targetSize:    chunkSize,
		mode:          mode,
		chunkBuffer:   make([]byte, 8),         // Reusable 8-byte buffer for chunk headers
		audioBuffer:   make([]byte, chunkSize), // Reusable audio buffer
		paddingBuffer: make([]byte, 1),         // Reusable padding buffer
		riffBuffer:    make([]byte, 12),        // Reusable RIFF header buffer
	}
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
	// Pre-allocate header buffer with reasonable estimate (typical WAV headers are ~100 bytes)
	headerBuffer := make([]byte, 0, 512)

	// Read RIFF header (12 bytes) - reuse buffer
	n, err := io.ReadFull(c.r, c.riffBuffer)
	if err != nil {
		return err
	}
	if n != 12 {
		return errors.New("incomplete RIFF header")
	}

	// Check RIFF signature using byte comparison
	if !compareID(c.riffBuffer[0:4], "RIFF") {
		return errors.New("not a valid WAV file: missing RIFF signature")
	}

	// Check WAVE signature using byte comparison
	if !compareID(c.riffBuffer[8:12], "WAVE") {
		return errors.New("not a valid WAV file: missing WAVE signature")
	}

	headerBuffer = append(headerBuffer, c.riffBuffer...)

	// Read chunks until we find the data chunk
	for {
		// Reuse the chunk buffer
		n, err := io.ReadFull(c.r, c.chunkBuffer)
		if err != nil {
			return err
		}
		if n != 8 {
			return errors.New("incomplete chunk header")
		}

		// Use byte comparison instead of string conversion
		isDataChunk := compareID(c.chunkBuffer[0:4], "data")
		chunkSize := readUint32LE(c.chunkBuffer[4:8])

		headerBuffer = append(headerBuffer, c.chunkBuffer...)

		if isDataChunk {
			// Found the data chunk
			c.dataSize = chunkSize
			c.header = headerBuffer
			c.dataStart = int64(len(headerBuffer))
			c.bytesRead = int64(len(headerBuffer))
			return nil
		}

		// Read and include the chunk data in the header
		// For large chunk data, we still need to allocate, but this is rare
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
			n, err = io.ReadFull(c.r, c.paddingBuffer)
			if err != nil && err != io.EOF {
				return err
			}
			if n == 1 {
				headerBuffer = append(headerBuffer, c.paddingBuffer...)
			}
		}
	}
}

// createCompleteWAVFile creates a complete WAV file from header and audio data
func (c *WAVChunker) createCompleteWAVFile(audioData []byte) []byte {
	headerLen := len(c.header)
	audioLen := len(audioData)
	totalLen := headerLen + audioLen

	// Allocate result buffer once
	result := make([]byte, totalLen)

	// Copy header
	copy(result, c.header)

	// Update the data chunk size (last 4 bytes of header)
	dataSize := writeUint32LE(uint32(audioLen))
	copy(result[headerLen-4:headerLen], dataSize)

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
			c.err = err
			return nil, err
		}
		c.headerSent = true
	}

	// Check if we've read all the audio data
	audioDataLeft := int64(c.dataSize) - (c.bytesRead - c.dataStart)
	if audioDataLeft <= 0 {
		return nil, io.EOF
	}

	// Read audio data for this chunk
	readSize := c.targetSize
	if c.mode == WAVModeComplete {
		// For complete mode, subtract header size from target to leave room for header
		readSize = c.targetSize - len(c.header)
		if readSize <= 0 {
			readSize = 1024 // minimum chunk size
		}
	}

	if int64(readSize) > audioDataLeft {
		readSize = int(audioDataLeft)
	}

	// Resize audioBuffer if needed
	if len(c.audioBuffer) < readSize {
		c.audioBuffer = make([]byte, readSize)
	}

	// Read directly into the reusable buffer
	n, err := c.r.Read(c.audioBuffer[:readSize])
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		c.err = err
		return nil, err
	}

	c.bytesRead += int64(n)
	audioData := c.audioBuffer[:n] // Slice the buffer to actual read size

	var chunk []byte

	// Create appropriate chunk based on mode
	switch c.mode {
	case WAVModeStreaming:
		// Original streaming behavior
		if c.bytesRead == c.dataStart+int64(n) {
			// First chunk: include header + audio data
			chunk = make([]byte, len(c.header)+len(audioData))
			copy(chunk, c.header)
			copy(chunk[len(c.header):], audioData)
		} else {
			// Subsequent chunks: just audio data - make a copy since we're reusing audioBuffer
			chunk = make([]byte, len(audioData))
			copy(chunk, audioData)
		}
	case WAVModeComplete:
		// Complete mode: each chunk is a complete WAV file
		chunk = c.createCompleteWAVFile(audioData)
	default:
		return nil, errors.New("unknown WAV chunk mode")
	}

	return chunk, nil
}
