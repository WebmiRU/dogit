# Build stage: compile the Nuxt SPA.
FROM node:24-alpine AS build

WORKDIR /app

# Dependencies are installed from the lockfile first so that source-only changes
# reuse the cached layer.
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
# "generate" prerenders the SPA shell (index.html). A plain "build" only emits
# the assets and leaves the shell for the Node server, which production does not
# run.
RUN npm run generate

# Runtime stage: only the static assets, served by nginx. Node is not part of the
# running image.
FROM nginx:1.29-alpine

COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80
