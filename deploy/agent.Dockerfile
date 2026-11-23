# syntax=docker/dockerfile:1
#
# A starting point for the image agent sessions run in when barnraise.yaml has
#
#   container:
#     image: barnraise-agent:local
#
# It needs the AI CLIs and whatever the project's checks and builds use; add
# your toolchain (Go, a JDK, the Android SDK…) on top. The barnraise binary,
# the item's worktree, the repository's .git, the agent's account and the
# daemon's socket are mounted by Barnraise at run time: nothing of them is
# baked in, and no secret belongs in this image.
#
#   docker build -f deploy/agent.Dockerfile -t barnraise-agent:local .
FROM node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl ripgrep jq make \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code @openai/codex \
 && npm cache clean --force
# Sessions run as the uid of the daemon's user, whatever it is: HOME is a
# folder Barnraise mounts, and git must accept the mounted worktree.
RUN git config --system --add safe.directory '*'
ENV DISABLE_AUTOUPDATER=1
