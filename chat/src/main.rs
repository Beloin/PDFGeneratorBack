mod db;
mod llm;
mod mcp;
mod routes;

use std::{env, sync::Arc};

use axum::Router;
use axum::extract::DefaultBodyLimit;
use axum::routing::{get, post};

use routes::AppState;

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt().init();

    let llm: Arc<dyn llm::LLM> = Arc::new(llm::OpenAiClient::from_env());
    let db = db::connect().await;
    let state = AppState { llm, db };

    let app = Router::new()
        .route("/health", get(routes::health))
        .route("/tools", get(routes::tools))
        .route("/call", post(routes::call))
        .route(
            "/chat",
            post(routes::chat).layer(DefaultBodyLimit::max(20 * 1024 * 1024)),
        )
        .route(
            "/chat/stream",
            post(routes::chat_stream).layer(DefaultBodyLimit::max(20 * 1024 * 1024)),
        )
        .route("/chat/history", get(routes::chat_history))
        .with_state(state);

    let addr = env::var("CHAT_LISTEN_ADDR").unwrap_or_else(|_| "0.0.0.0:3000".into());
    let listener = tokio::net::TcpListener::bind(&addr).await.unwrap();
    tracing::info!("chat harness listening on {addr}");
    axum::serve(listener, app).await.unwrap();
}
