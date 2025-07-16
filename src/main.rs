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
        .get_matches();

    let playback_format = matches.get_one::<String>("playback").unwrap();
    let verbose = matches.get_flag("verbose");
    
    if verbose {
        println!("Using playback format: {}", playback_format);
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
                        successful_decode_count += 1;
                        if tx.send(decoded_data).is_err() {
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
