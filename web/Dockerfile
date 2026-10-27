# Boxaz Web — Next.js standalone
FROM node:22-alpine AS deps
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

FROM node:22-alpine AS build
WORKDIR /app
RUN corepack enable
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ARG NEXT_PUBLIC_BOXAZ_API=http://localhost:8080/v1
ARG NEXT_PUBLIC_BOXAZ_WS=
ENV NEXT_PUBLIC_BOXAZ_API=$NEXT_PUBLIC_BOXAZ_API NEXT_PUBLIC_BOXAZ_WS=$NEXT_PUBLIC_BOXAZ_WS NEXT_TELEMETRY_DISABLED=1
RUN pnpm build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
RUN addgroup -S boxaz && adduser -S boxaz -G boxaz
COPY --from=build --chown=boxaz:boxaz /app/.next/standalone ./
COPY --from=build --chown=boxaz:boxaz /app/.next/static ./.next/static
COPY --from=build --chown=boxaz:boxaz /app/public ./public
USER boxaz
EXPOSE 3000
# BOXAZ_API (URL Core) và BOXAZ_MOCK đọc lúc chạy
CMD ["node", "server.js"]
