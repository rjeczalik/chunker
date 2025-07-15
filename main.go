package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const bufferSize = 4096

type DataChunk struct {
	Data string `json:"data"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <file>\n", os.Args[0])
		os.Exit(1)
	}

	filePath := os.Args[1]
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	buffer := make([]byte, bufferSize)
	encoder := json.NewEncoder(os.Stdout)

	for {
		bytesRead, err := file.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}

		if bytesRead > 0 {
			chunk := buffer[:bytesRead]
			encodedData := base64.StdEncoding.EncodeToString(chunk)
			dataChunk := DataChunk{Data: encodedData}

			if err := encoder.Encode(dataChunk); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
				os.Exit(1)
			}
			fmt.Println()
		}
	}
}
