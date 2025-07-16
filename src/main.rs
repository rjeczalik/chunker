use anyhow::Result;
use base64::{engine::general_purpose, Engine as _};
use clap::{Arg, Command};
use flate2::read::GzDecoder;
use rodio::{Decoder, OutputStream, Sink};
use serde::Deserialize;
use std::io::{self, BufRead, Cursor, Read};
use std::sync::mpsc;
use std::thread;

#[derive(Deserialize)]
struct JsonData {
    data: String,
}

fn main() -> Result<()> {
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
        .arg(
            Arg::new("verbose")
                .long("verbose")
                .short('v')
                .help("Enable verbose output")
                .action(clap::ArgAction::SetTrue)
        )
        .arg(
            Arg::new("gzip")
                .long("gzip")
                .help("Decompress gzip-compressed audio chunks")
                .action(clap::ArgAction::SetTrue)
        )
        .get_matches();

    let playback_format = matches.get_one::<String>("playback").unwrap();
    let verbose = matches.get_flag("verbose");
    let decompress_gzip = matches.get_flag("gzip");
    
    if verbose {
        println!("Using playback format: {}", playback_format);
        if decompress_gzip {
            println!("Gzip decompression enabled");
        }
    }
    
    let (_stream, stream_handle) = OutputStream::try_default()?;
    let sink = Sink::try_new(&stream_handle)?;
    
    if verbose {
        println!("Audio output initialized");
    }

    let (tx, rx) = mpsc::channel::<Vec<u8>>();

    let format = playback_format.clone();
    let consumer_thread = thread::spawn(move || {
        let mut chunk_count = 0;
        let mut successful_chunks = 0;
        
        for decoded_data in rx {
            chunk_count += 1;
            
            if verbose {
                println!("Processing audio chunk {}, size: {} bytes", chunk_count, decoded_data.len());
            }
            
            let audio_file = Cursor::new(decoded_data);
            let source = match format.as_str() {
                "mp3" => Decoder::new_mp3(audio_file),
                "wav" => Decoder::new_wav(audio_file),
                _ => unreachable!("Invalid format should be caught by clap"),
            };
            
            match source {
                Ok(source) => {
                    successful_chunks += 1;
                    if verbose {
                        println!("Successfully decoded audio chunk {}", chunk_count);
                    }
                    sink.append(source);
                }
                Err(e) => {
                    eprintln!("Failed to decode audio chunk {}: {}", chunk_count, e);
                }
            }
        }
        
        if verbose {
            println!("Processed {} audio chunks total ({} successful)", chunk_count, successful_chunks);
        }
        
        // Wait for the last sound to finish playing.
        sink.sleep_until_end();
        
        if verbose {
            println!("Audio playback finished");
        }
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
                        // Decompress if gzip flag is set
                        let final_data = if decompress_gzip {
                            let mut gz = GzDecoder::new(Cursor::new(&decoded_data));
                            let mut decompressed = Vec::new();
                            match gz.read_to_end(&mut decompressed) {
                                Ok(_) => {
                                    if verbose {
                                        println!("Decompressed chunk from {} to {} bytes", decoded_data.len(), decompressed.len());
                                    }
                                    decompressed
                                }
                                Err(e) => {
                                    eprintln!("Failed to decompress gzip data on line {}: {}", line_count, e);
                                    continue;
                                }
                            }
                        } else {
                            decoded_data
                        };

                        successful_decode_count += 1;
                        if tx.send(final_data).is_err() {
                            break;
                        }
                    }
                    Err(e) => {
                        eprintln!("Failed to decode base64 data on line {}: {}", line_count, e);
                    }
                }
            }
            Err(e) => {
                eprintln!("Failed to parse JSON on line {}: {}", line_count, e);
            }
        }
    }

    if verbose {
        println!("Input processing complete:");
        println!("  Total lines: {}", line_count);
        println!("  Valid JSON lines: {}", valid_json_count);
        println!("  Successfully decoded chunks: {}", successful_decode_count);
    }

    drop(tx);

    consumer_thread.join().expect("Consumer thread panicked");

    Ok(())
}
