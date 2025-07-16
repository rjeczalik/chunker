package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

type DataChunk struct {
	Data string `json:"data"`
}

func main() {
	var blockSize int
	flag.IntVar(&blockSize, "b", 8192, "block size for chunking")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-b blocksize] <file>\n", os.Args[0])
		os.Exit(1)
	}

	filename := flag.Arg(0)

	file, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Create chunker with specified block size and larger reservoir size
	chunker := NewChunker(file, blockSize, 2048) // Use larger reservoir

	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error chunking file: %v\n", err)
			os.Exit(1)
		}

		dataChunk := DataChunk{
			Data: base64.StdEncoding.EncodeToString(chunk),
		}
		if err := json.NewEncoder(os.Stdout).Encode(dataChunk); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
			os.Exit(1)
		}
	}
}
