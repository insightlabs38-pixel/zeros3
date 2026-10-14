// z3preflate: minimal CLI over preflate-rs' whole-stream API for the Z3 oracle harness.
//   z3preflate analyze <deflate-in> <plain-out> <recon-out> [max_chain]
//   z3preflate recreate <plain-in> <recon-in> <deflate-out>
// One JSON line on stdout; exit status is 0 even for preflate-level failure (ok=false).
use preflate_rs::{preflate_whole_deflate_stream, recreate_whole_deflate_stream, PreflateConfig};
use std::time::Instant;

fn esc(s: &str) -> String {
    s.chars().map(|c| match c { '"' => "\\\"".into(), '\\' => "\\\\".into(), '\n' | '\r' => " ".into(), c => c.to_string() }).collect()
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() < 5 { eprintln!("usage: z3preflate analyze|recreate ..."); std::process::exit(2); }
    match a[1].as_str() {
        "analyze" => {
            let data = std::fs::read(&a[2]).expect("read input");
            let mut cfg = PreflateConfig::default();
            cfg.verify_compression = false;
            cfg.plain_text_limit = 1 << 30;
            if a.len() > 5 { cfg.max_chain_length = a[5].parse().expect("max_chain"); }
            let t = Instant::now();
            match preflate_whole_deflate_stream(&data, &cfg) {
                Ok((r, plain)) => {
                    let ns = t.elapsed().as_nanos();
                    std::fs::write(&a[3], plain.text()).expect("write plain");
                    std::fs::write(&a[4], &r.corrections).expect("write recon");
                    println!("{{\"ok\":true,\"consumed\":{},\"plain_len\":{},\"recon_len\":{},\"ns\":{},\"params\":\"{}\"}}",
                        r.compressed_size, plain.text().len(), r.corrections.len(), ns,
                        esc(&format!("{:?}", r.parameters)));
                }
                Err(e) => println!("{{\"ok\":false,\"ns\":{},\"err\":\"{}\"}}", t.elapsed().as_nanos(), esc(&format!("{}", e))),
            }
        }
        "recreate" => {
            let plain = std::fs::read(&a[2]).expect("read plain");
            let recon = std::fs::read(&a[3]).expect("read recon");
            let t = Instant::now();
            match recreate_whole_deflate_stream(&plain, &recon) {
                Ok(out) => {
                    let ns = t.elapsed().as_nanos();
                    std::fs::write(&a[4], &out).expect("write out");
                    println!("{{\"ok\":true,\"out_len\":{},\"ns\":{}}}", out.len(), ns);
                }
                Err(e) => println!("{{\"ok\":false,\"ns\":{},\"err\":\"{}\"}}", t.elapsed().as_nanos(), esc(&format!("{}", e))),
            }
        }
        _ => { eprintln!("unknown command"); std::process::exit(2); }
    }
}
