# syntax=docker/dockerfile:1
#
# web image (docker-compose `core` profile, issue #100).
#
# Multi-stage: a Node stage builds the Vite + React + TS app to static assets, then
# an nginx stage serves them AND reverse-proxies the engine surface (/api -> engine
# REST, /stream -> engine WebSocket) so the browser only talks to its own origin —
# the engine base is never baked into the bundle. The upstream is configurable via
# the ENGINE_UPSTREAM env (default the compose service `engine:8080`), templated
# into the nginx config at container start by the official image's envsubst step.

# ---- build stage: compile the SPA (produces dist/, incl. index.html) ----------
FROM node:20.19-alpine AS build
WORKDIR /app

# Install deps against the lockfile first for cache-friendly layers.
COPY package.json package-lock.json ./
RUN npm ci

# Build the app: tsc -b + vite build. Emits the hashed assets and index.html.
COPY . .
RUN npm run build

# ---- serve stage: nginx serving the built dist/ + proxying the engine ---------
FROM nginx:1.27-alpine

# Default engine upstream (compose service name). Override at run time if needed.
ENV ENGINE_UPSTREAM=engine:8080

# The official nginx image runs envsubst over /etc/nginx/templates/*.template at
# start, substituting ${ENGINE_UPSTREAM} and writing the result to /etc/nginx/conf.d/.
COPY nginx.conf /etc/nginx/templates/default.conf.template

# Copy the built static site (the Vite build output, including index.html).
COPY --from=build /app/dist/ /usr/share/nginx/html/

EXPOSE 80
# nginx:alpine's default CMD already runs nginx in the foreground.
