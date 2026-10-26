// Z3-26 research producer: writes a dynamic or differencing VHDX from a raw logical state using the
// pinned `vhdx-rs` crate. Isolated from the Go module; never a ZeroS3 dependency.
//
// usage: vhdxgen --out F --src RAW [--size N] [--block B] [--lss 4096] [--parent P --parent-rel R --parent-raw RAW]
//                [--order asc|desc|shuf] [--seed N]
use std::fs::{File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use vhdx::Medium;

const SEC: usize = 4096;
const MAXRUN: usize = 256; // sectors per write call (1 MiB)

fn arg(a: &[String], k: &str) -> Option<String> {
    a.iter().position(|x| x == k).map(|i| a[i + 1].clone())
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    let out = arg(&a, "--out").expect("--out");
    let src = arg(&a, "--src").expect("--src");
    let block: u32 = arg(&a, "--block").map(|s| s.parse().unwrap()).unwrap_or(2 << 20);
    let lss: u32 = arg(&a, "--lss").map(|s| s.parse().unwrap()).unwrap_or(4096);
    let order = arg(&a, "--order").unwrap_or("asc".into());
    let seed: u64 = arg(&a, "--seed").map(|s| s.parse().unwrap()).unwrap_or(1);
    let mut sf = File::open(&src).unwrap();
    let size = sf.metadata().unwrap().len();
    let inner = OpenOptions::new().read(true).write(true).create(true).truncate(true).open(&out).unwrap();
    let mut co = Medium::create(inner).size(size).block_size(block).logical_sector_size(lss).physical_sector_size(4096);
    let mut praw: Option<File> = None;
    if let Some(p) = arg(&a, "--parent") {
        let rel = arg(&a, "--parent-rel").unwrap_or(p.clone());
        let mut pm = Medium::open(File::open(&p).unwrap()).finish().unwrap();
        co = co.parent(&mut pm, rel).unwrap();
        praw = Some(File::open(arg(&a, "--parent-raw").expect("--parent-raw")).unwrap());
    }
    co.finish().unwrap();
    let inner = OpenOptions::new().read(true).write(true).open(&out).unwrap();
    let mut m = Medium::open(inner).write().finish().unwrap();

    // collect runs of sectors to write
    let n = (size as usize) / SEC;
    let mut runs: Vec<(usize, usize)> = vec![];
    let (mut b1, mut b2) = (vec![0u8; SEC], vec![0u8; SEC]);
    let mut cur: Option<(usize, usize)> = None;
    sf.seek(SeekFrom::Start(0)).unwrap();
    for i in 0..n {
        sf.read_exact(&mut b1).unwrap();
        let want = match praw.as_mut() {
            Some(p) => {
                p.read_exact(&mut b2).unwrap();
                b1 != b2
            }
            None => b1.iter().any(|&x| x != 0),
        };
        match (want, cur) {
            (true, Some((s, c))) if c < MAXRUN && s + c == i => cur = Some((s, c + 1)),
            (true, Some(r)) => {
                runs.push(r);
                cur = Some((i, 1));
            }
            (true, None) => cur = Some((i, 1)),
            (false, Some(r)) => {
                runs.push(r);
                cur = None;
            }
            _ => {}
        }
    }
    if let Some(r) = cur {
        runs.push(r);
    }
    match order.as_str() {
        "desc" => runs.reverse(),
        "shuf" => {
            let mut x = seed.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
            for i in (1..runs.len()).rev() {
                x = x.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
                runs.swap(i, (x >> 33) as usize % (i + 1));
            }
        }
        _ => {}
    }
    let mut io = m.io().unwrap();
    for (s, c) in &runs {
        let mut buf = vec![0u8; c * SEC];
        sf.seek(SeekFrom::Start((*s * SEC) as u64)).unwrap();
        sf.read_exact(&mut buf).unwrap();
        let mut sec = io.sector(*s as u64, *c as u64).unwrap();
        sec.write_all(&buf).unwrap();
    }
    eprintln!("vhdxgen: {} runs, {} sectors", runs.len(), runs.iter().map(|r| r.1).sum::<usize>());
}
