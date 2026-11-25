FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
ARG NEXT_PUBLIC_API=http://localhost:18100
ARG NEXT_PUBLIC_CHAOS=http://localhost:18110
ENV NEXT_PUBLIC_API=$NEXT_PUBLIC_API NEXT_PUBLIC_CHAOS=$NEXT_PUBLIC_CHAOS NEXT_TELEMETRY_DISABLED=1
RUN npm run build

# Image minimale : serveur Next autonome (output: "standalone"), utilisateur non privilégié.
FROM node:24-alpine
WORKDIR /app
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0 NEXT_TELEMETRY_DISABLED=1
COPY --from=build --chown=node /app/.next/standalone ./
COPY --from=build --chown=node /app/.next/static ./.next/static
USER node
EXPOSE 3000
CMD ["node", "server.js"]
