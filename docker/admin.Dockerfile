# Multi-stage build for Admin UI (Node.js/Astro)
FROM node:22-alpine3.21 AS builder

WORKDIR /build

# Copy package manifests
COPY admin-ui/package.json admin-ui/package-lock.json* ./

# Install production dependencies only
RUN npm ci --omit=dev

# Copy source code
COPY admin-ui/ ./

# Build application
RUN npm run build

# Runtime stage
FROM nginx:1.27-alpine

LABEL org.opencontainers.image.description="GitStore admin web interface for managing products, categories, collections, and catalog publishing"

# Serve static output on port 3000 to match compose mapping.
RUN printf 'server {\n    listen 3000;\n    server_name _;\n\n    root /usr/share/nginx/html;\n    index index.html;\n\n    location / {\n        try_files $uri $uri/ /index.html;\n    }\n}\n' > /etc/nginx/conf.d/default.conf

# Copy built application
COPY --from=builder /build/dist/ /usr/share/nginx/html/

# Expose Admin UI port
EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]