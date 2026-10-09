# The web tier: the single-page application, built and served by nginx.
#
# Not the Go application and not a copy of it. The application has its own image and its
# own deployment and its own database connection; this one is the front door, needs
# nothing from a database, and is asked for one file: /index.html.
#
# The nginx configuration travels in the image because the compose stack mounts none.
# Kubernetes mounts its own over the same path from a ConfigMap, with the cluster's DNS
# address in it rather than Docker's, so the file here is the fallback and not the
# winner there.

FROM node:22-alpine AS build

WORKDIR /src

# The manifest alone, so a change to a source file does not spend the install again.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

FROM nginx:1.25

COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80