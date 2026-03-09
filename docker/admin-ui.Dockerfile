# Multi-stage build for Admin UI (Node.js/Astro)
FROM node:18-alpine as builder

WORKDIR /build

# Copy package manifests
COPY admin-ui/package.json admin-ui/package-lock.json* ./

# Install dependencies
RUN npm ci

# Copy source code
COPY admin-ui/ ./

# Build application
RUN npm run build

# Runtime stage
FROM node:18-alpine

WORKDIR /app

# Copy built application
COPY --from=builder /build/dist ./dist
COPY --from=builder /build/package.json ./
COPY --from=builder /build/node_modules ./node_modules

# Expose Admin UI port
EXPOSE 3000

ENV GITSTORE_GRAPHQL_URL=http://api:4000/graphql
ENV NODE_ENV=production

CMD ["npm", "run", "preview", "--", "--host", "0.0.0.0", "--port", "3000"]
