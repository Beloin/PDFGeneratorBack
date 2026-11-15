# Backend aggregator: builds every service. `heph run //:backend`.
target(
    name = "backend",
    driver = "group",
    deps = [
        "//server/cmd/server:build",
        "//chat:chat",
        "//gen:gen",
    ],
)
