mod db;
mod llm;
mod mcp;
mod routes;

use std::sync::Arc;
use std::{
    env,
    path::{Path, PathBuf},
};

use axum::Router;
use axum::routing::{get, post};

use routes::AppState;

fn find_env_file(requested: &Path, cwd: &Path, executable: &Path) -> Option<PathBuf> {
    if requested.is_absolute() {
        return requested.is_file().then(|| requested.to_path_buf());
    }

    let workspace = executable
        .ancestors()
        .find(|dir| dir.join(".hephconfig").is_file());
    for root in [Some(cwd), workspace].into_iter().flatten() {
        for path in [root.join(requested), root.join("chat").join(requested)] {
            if path.is_file() {
                return Some(path);
            }
        }
    }
    None
}

fn load_env() {
    let explicit = env::var_os("CHAT_ENV_FILE");
    let requested = explicit
        .as_deref()
        .map(Path::new)
        .unwrap_or(Path::new(".env"));
    let cwd = env::current_dir().unwrap_or_default();
    let executable = env::current_exe().unwrap_or_default();
    match find_env_file(requested, &cwd, &executable) {
        Some(path) => {
            dotenvy::from_path(&path)
                .unwrap_or_else(|e| panic!("load env file {}: {e}", path.display()));
            tracing::info!(path = %path.display(), "loaded LLM env");
        }
        None if explicit.is_some() => panic!("CHAT_ENV_FILE not found: {}", requested.display()),
        None => {}
    }
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt().init();
    load_env();

    let llm: Arc<dyn llm::LLM> = Arc::new(llm::OpenAiClient::from_env());
    let db = db::connect().await;
    let state = AppState { llm, db };

    let app = Router::new()
        .route("/health", get(routes::health))
        .route("/tools", get(routes::tools))
        .route("/call", post(routes::call))
        .route("/chat", post(routes::chat))
        .route("/chat/history", get(routes::chat_history))
        .with_state(state);

    let addr = "0.0.0.0:3000";
    let listener = tokio::net::TcpListener::bind(addr).await.unwrap();
    tracing::info!("chat harness listening on {addr}");
    axum::serve(listener, app).await.unwrap();
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn finds_env_files_from_heph_sandbox() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let root = env::temp_dir().join(format!("chat-env-{}-{nonce}", std::process::id()));
        let chat = root.join("chat");
        fs::create_dir_all(&chat).unwrap();
        fs::write(root.join(".hephconfig"), "").unwrap();
        fs::write(chat.join(".env"), "LLM_MODEL=test\n").unwrap();
        fs::write(chat.join(".env.claude"), "LLM_MODEL=test\n").unwrap();

        let sandbox = root.join(".heph/sandbox/chat/__target_run/ws/chat");
        let executable = sandbox.join("chat");
        assert_eq!(
            find_env_file(Path::new(".env"), &sandbox, &executable),
            Some(chat.join(".env"))
        );
        assert_eq!(
            find_env_file(Path::new(".env.claude"), &sandbox, &executable),
            Some(chat.join(".env.claude"))
        );

        fs::remove_dir_all(root).unwrap();
    }
}
