//! A hostile build script for the builder's test (ADR-0064). It follows
//! `escape.txt`, which the test writes beside it, one step per line:
//!
//! - `write <path>`: create the path's folder and write a file there;
//! - `write-tmp <name>`: the same in `$TMPDIR`, inside the build's own folder;
//! - `detach <seconds>`: start `setsid sleep <seconds>` and leave it running.
//!
//! It reports each step as a cargo warning and never fails the build, so the
//! test sees what was refused and what was left behind.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

fn main() {
    println!("cargo:rerun-if-changed=escape.txt");
    let dir = env::var("CARGO_MANIFEST_DIR").unwrap_or_default();
    let plan = fs::read_to_string(Path::new(&dir).join("escape.txt")).unwrap_or_default();
    for line in plan.lines() {
        match line.split_once(' ') {
            Some(("write", path)) => write(PathBuf::from(path)),
            Some(("write-tmp", name)) => write(Path::new(&env::var("TMPDIR").unwrap_or_default()).join(name)),
            Some(("detach", secs)) => detach(secs),
            _ => {}
        }
    }
}

fn write(path: PathBuf) {
    if let Some(parent) = path.parent() {
        let _ = fs::create_dir_all(parent);
    }
    match fs::write(&path, "escaped") {
        Ok(()) => println!("cargo:warning=wrote {}", path.display()),
        Err(e) => println!("cargo:warning=refused {}: {}", path.display(), e),
    }
}

fn detach(secs: &str) {
    let started = Command::new("setsid")
        .arg("sleep")
        .arg(secs)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn();
    match started {
        Ok(child) => println!("cargo:warning=detached sleep {} as {}", secs, child.id()),
        Err(e) => println!("cargo:warning=could not detach: {}", e),
    }
}
