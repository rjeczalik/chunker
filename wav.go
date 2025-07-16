package main

import (
	"errors"
	"io"
)

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
}

// NewWAVChunker returns a new WAVChunker that reads from r.
func NewWAVChunker(r io.Reader, chunkSize int, mode WAVChunkMode) *WAVChunker {
	return &WAVChunker{
		r:          r,
		targetSize: chunkSize,
		mode:       mode,
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

// createCompleteWAVFile creates a complete WAV file from header and audio data
func (c *WAVChunker) createCompleteWAVFile(audioData []byte) []byte {
	// Create a copy of the header up to the data chunk size field
	headerCopy := make([]byte, len(c.header))
	copy(headerCopy, c.header)

	// Update the data chunk size (last 4 bytes of header)
	dataSize := writeUint32LE(uint32(len(audioData)))
	copy(headerCopy[len(headerCopy)-4:], dataSize)

	// Update the overall file size in RIFF header (at offset 4)
	totalSize := len(headerCopy) + len(audioData) - 8 // -8 for RIFF header itself
	riffSize := writeUint32LE(uint32(totalSize))
	copy(headerCopy[4:8], riffSize)

	// Combine header and audio data
	result := make([]byte, len(headerCopy)+len(audioData))
	copy(result, headerCopy)
	copy(result[len(headerCopy):], audioData)

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

	audioData := make([]byte, readSize)
	n, err := c.r.Read(audioData)
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		c.err = err
		return nil, err
	}

	c.bytesRead += int64(n)
	audioData = audioData[:n]

	// Return appropriate chunk based on mode
	switch c.mode {
	case WAVModeStreaming:
		// Original streaming behavior
		if c.bytesRead == c.dataStart+int64(n) {
			// First chunk: include header + audio data
			chunk := make([]byte, len(c.header)+len(audioData))
			copy(chunk, c.header)
			copy(chunk[len(c.header):], audioData)
			return chunk, nil
		} else {
			// Subsequent chunks: just audio data
			return audioData, nil
		}
	case WAVModeComplete:
		// Complete mode: each chunk is a complete WAV file
		return c.createCompleteWAVFile(audioData), nil
	default:
		return nil, errors.New("unknown WAV chunk mode")
	}
}
