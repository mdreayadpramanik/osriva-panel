# OSRIVA Panel dashboard — Next.js standalone output, non-root.
# Build from the repository root:  docker build -f infrastructure/docker/dashboard.Dockerfile .
FROM node:22-alpine AS deps
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/dashboard/package.json apps/dashboard/
RUN npm ci --workspace @osriva/dashboard --include-workspace-root=false

FROM node:22-alpine AS build
WORKDIR /src
COPY --from=deps /src/node_modules ./node_modules
COPY package.json package-lock.json ./
COPY apps/dashboard ./apps/dashboard
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build -w @osriva/dashboard

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
RUN addgroup -S osriva && adduser -S osriva -G osriva
COPY --from=build --chown=osriva:osriva /src/apps/dashboard/.next/standalone ./
COPY --from=build --chown=osriva:osriva /src/apps/dashboard/.next/static ./apps/dashboard/.next/static
USER osriva
EXPOSE 3000
CMD ["node", "apps/dashboard/server.js"]
