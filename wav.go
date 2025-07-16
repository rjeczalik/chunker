package main

import (
	"errors"
	"io"
)

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
