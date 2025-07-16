package main

import (
	"io"
)

// Chunker interface for different audio file types
type Chunker interface {
	Next() ([]byte, error)
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
