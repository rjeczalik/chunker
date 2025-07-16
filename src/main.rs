use anyhow::Result;
use base64::{engine::general_purpose, Engine as _};
use clap::{Arg, Command};
use rodio::{Decoder, OutputStream, Sink};
use serde::Deserialize;
use std::io::{self, BufRead, Cursor};
use std::sync::mpsc;
use std::thread;

#[derive(Deserialize)]
struct JsonData {
    data: String,
}

// Helper function to read little-endian u32
fn read_u32_le(data: &[u8]) -> u32 {
    u32::from_le_bytes([data[0], data[1], data[2], data[3]])
}

// Helper function to write little-endian u32
fn write_u32_le(value: u32) -> [u8; 4] {
    value.to_le_bytes()
}

// Extract WAV header information from the first chunk
fn extract_wav_header(data: &[u8]) -> Option<(Vec<u8>, usize)> {
    if data.len() < 12 {
        return None;
    }
    
    // Check for RIFF header
    if &data[0..4] != b"RIFF" || &data[8..12] != b"WAVE" {
        return None;
    }
    
    let mut pos = 12;
    let mut header = Vec::new();
    header.extend_from_slice(&data[0..12]); // RIFF header
    
    // Find the data chunk
    while pos + 8 <= data.len() {
        let chunk_id = &data[pos..pos+4];
        let chunk_size = read_u32_le(&data[pos+4..pos+8]);
        
        header.extend_from_slice(&data[pos..pos+8]); // chunk header
        
        if chunk_id == b"data" {
            // Found data chunk, return header up to this point
            return Some((header, pos + 8));
        }
        
        // Include the chunk data in header
        let chunk_data_end = pos + 8 + chunk_size as usize;
        if chunk_data_end > data.len() {
            break;
        }
        
        header.extend_from_slice(&data[pos+8..chunk_data_end]);
        pos = chunk_data_end;
        
        // Handle padding for odd-sized chunks
        if chunk_size % 2 == 1 && pos < data.len() {
            header.push(data[pos]);
            pos += 1;
        }
    }
    
    None
}

// Reconstruct a complete WAV file from header and audio data
fn reconstruct_wav_file(header: &[u8], audio_data: &[u8]) -> Vec<u8> {
    let mut result = Vec::new();
    
    // Copy header but we need to update the data chunk size
    let mut header_copy = header.to_vec();
    
    // Update the data chunk size (last 4 bytes of header should be the data size)
    if header_copy.len() >= 4 {
        let data_size_bytes = write_u32_le(audio_data.len() as u32);
        let header_len = header_copy.len();
        header_copy[header_len-4..header_len].copy_from_slice(&data_size_bytes);
    }
    
    // Update the overall file size in RIFF header
    let total_size = header_copy.len() + audio_data.len() - 8; // -8 for RIFF header itself
    if header_copy.len() >= 8 {
        let riff_size_bytes = write_u32_le(total_size as u32);
        header_copy[4..8].copy_from_slice(&riff_size_bytes);
    }
    
    result.extend_from_slice(&header_copy);
    result.extend_from_slice(audio_data);
    
    result
}

fn main() -> Result<()> {
    tracing_subscriber::fmt::init();
    
    let matches = Command::new("jsonl_player")
        .version("1.0")
        .author("Your Name")
        .about("Plays audio chunks from JSONL stream")
        .arg(
            Arg::new("playback")
                .long("playback")
                .value_name("FORMAT")
                .help("Audio format for playback")
                .value_parser(["mp3", "wav"])
                .default_value("mp3")
        )
        .get_matches();

    let playback_format = matches.get_one::<String>("playback").unwrap();
    println!("Using playback format: {}", playback_format);
    
    let (_stream, stream_handle) = OutputStream::try_default()?;
    let sink = Sink::try_new(&stream_handle)?;
    println!("Audio output initialized");

    let (tx, rx) = mpsc::channel::<Vec<u8>>();

    let format = playback_format.clone();
    let consumer_thread = thread::spawn(move || {
        let mut chunk_count = 0;
        let mut wav_header: Option<Vec<u8>> = None;
        
        for decoded_data in rx {
            chunk_count += 1;
            println!("Processing audio chunk {}, size: {} bytes", chunk_count, decoded_data.len());
            
            let audio_data = if format == "wav" {
                if chunk_count == 1 {
                    // First chunk: extract header and use the complete chunk
                    if let Some((header, data_start)) = extract_wav_header(&decoded_data) {
                        wav_header = Some(header);
                        println!("Extracted WAV header from first chunk");
                        decoded_data
                    } else {
                        println!("Failed to extract WAV header from first chunk");
                        decoded_data
                    }
                } else {
                    // Subsequent chunks: reconstruct complete WAV file
                    if let Some(ref header) = wav_header {
                        println!("Reconstructing WAV file for chunk {}", chunk_count);
                        reconstruct_wav_file(header, &decoded_data)
                    } else {
                        println!("No WAV header available for chunk {}", chunk_count);
                        decoded_data
                    }
                }
            } else {
                decoded_data
            };
            
            let audio_file = Cursor::new(audio_data);
            let source = match format.as_str() {
                "mp3" => Decoder::new_mp3(audio_file),
                "wav" => Decoder::new_wav(audio_file),
                _ => unreachable!("Invalid format should be caught by clap"),
            };
            
            match source {
                Ok(source) => {
                    println!("Successfully decoded audio chunk {}", chunk_count);
                    sink.append(source);
                }
                Err(e) => {
                    println!("Failed to decode audio chunk {}: {}", chunk_count, e);
                }
            }
        }
        println!("Processed {} audio chunks total", chunk_count);
        // Wait for the last sound to finish playing.
        sink.sleep_until_end();
        println!("Audio playback finished");
    });

    let stdin = io::stdin();
    let handle = stdin.lock();

    let mut line_count = 0;
    let mut valid_json_count = 0;
    let mut successful_decode_count = 0;

    for line in handle.lines() {
        let line = match line {
            Ok(line) => line,
            Err(_) => break,
        };

        line_count += 1;

        if line.trim().is_empty() {
            continue;
        }

        match serde_json::from_str::<JsonData>(&line) {
            Ok(json_data) => {
                valid_json_count += 1;
                match general_purpose::STANDARD.decode(&json_data.data) {
                    Ok(decoded_data) => {
                        successful_decode_count += 1;
                        if tx.send(decoded_data).is_err() {
                            break;
                        }
                    }
                    Err(e) => {
                        println!("Failed to decode base64 data on line {}: {}", line_count, e);
                    }
                }
            }
            Err(e) => {
                println!("Failed to parse JSON on line {}: {}", line_count, e);
            }
        }
    }

    println!("Input processing complete:");
    println!("  Total lines: {}", line_count);
    println!("  Valid JSON lines: {}", valid_json_count);
    println!("  Successfully decoded chunks: {}", successful_decode_count);

    drop(tx);

    consumer_thread.join().expect("Consumer thread panicked");

    Ok(())
}