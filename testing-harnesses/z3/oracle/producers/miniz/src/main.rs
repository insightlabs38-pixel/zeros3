use std::io::{Read, Write};
fn main() {
    let level: u8 = std::env::args().nth(1).expect("level").parse().expect("level");
    let mut b = Vec::new();
    std::io::stdin().read_to_end(&mut b).unwrap();
    let out = miniz_oxide::deflate::compress_to_vec(&b, level);
    std::io::stdout().write_all(&out).unwrap();
}
