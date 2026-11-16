target(
    name = "chat",
    driver = "bash",
    deps = [
        file("Cargo.toml"),
        file("Cargo.lock"),
        glob("src/**/*.rs"),
    ],
    tools = [
        "//@heph/bin:cargo",
        "//@heph/bin:rustc",
        "//@heph/bin:cc",
        "//@heph/bin:as",
        "//@heph/bin:ar",
        "//@heph/bin:ld",
    ],
    pass_env = ["HOME"],
    run = 'CARGO_HOME="$HOME/.cargo" cargo build --release --locked --offline --quiet && cp target/release/chat "$OUT"',
    out = "chat",
)

# Configure the runtime profile here; the binary reads only environment variables.
target(
    name = "run",
    driver = "bash",
    cache = False,
    deps = {
        "bin": ":chat",
        "envfile": file(".env"),
    },
    pass_env = [
        "CHAT_LISTEN_ADDR",
        "MCP_SERVER_URL",
        "AUTH_TOKEN",
        "DATABASE_URL",
        "LLM_BASE_URL",
        "LLM_MODEL",
        "LLM_API_KEY",
        "LLM_REASONING_EFFORT",
        "ANTHROPIC_WORKSPACE_ID",
        "RUST_LOG",
    ],
    run = '''
set -a
source "$SRC_ENVFILE"
set +a
exec "$SRC_BIN"
''',
)
