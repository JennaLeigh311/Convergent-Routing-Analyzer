# syntax=docker/dockerfile:1
#
# web image (docker-compose `core` profile, issue #6).
# Phase-0 placeholder: web/ is currently just a README + a static index.html
# (no React app yet — that lands in Phase 10). We serve the placeholder page
# with nginx so the container responds 200 on / and is genuinely healthy.
#
# When the React app exists, this becomes a multi-stage build (node build ->
# nginx serve); for now it is a single static stage.
FROM nginx:1.27-alpine

# Serve the Phase-0 placeholder page.
COPY index.html /usr/share/nginx/html/index.html

EXPOSE 80
# nginx:alpine's default CMD already runs nginx in the foreground.
