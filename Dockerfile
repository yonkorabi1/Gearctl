FROM node:20-alpine AS builder

WORKDIR /app

# ssh2's optional native crypto/cpu-features bindings need a build
# toolchain on alpine; falls back to pure-JS crypto if compilation fails,
# but installing these keeps AES acceleration available.
RUN apk add --no-cache python3 make g++

COPY package.json package-lock.json* ./
RUN npm ci

COPY tsconfig.json ./
COPY src/ ./src/

RUN npm run build

FROM node:20-alpine AS runner

WORKDIR /app

RUN addgroup -g 1001 -S nodejs && \
    adduser -S oltgw -u 1001

COPY --from=builder /app/package.json ./
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/dist ./dist

USER oltgw

EXPOSE 8090

CMD ["node", "dist/main.js"]
