target(
    name = "chat",
    driver = "bash",
    deps = [file("Cargo.toml"), file("Cargo.lock"), glob("src/**/*.rs")],
    tools = ["//@heph/bin:cargo", "//@heph/bin:rustc", "//@heph/bin:cc", "//@heph/bin:as", "//@heph/bin:ar", "//@heph/bin:ld"],
    pass_env = ["HOME"],
    run = 'CARGO_HOME="$HOME/.cargo" cargo build --release --locked --offline --quiet && cp target/release/chat "$OUT"',
    out = "chat",
)

# Build the binary then run it (blocks; Ctrl-C stops it).
target(
    name = "run",
    driver = "bash",
    cache = False,
    deps = {"bin": ":chat"},
    pass_env = ["CHAT_ENV_FILE"],
    run = '"$SRC_BIN"',
)
