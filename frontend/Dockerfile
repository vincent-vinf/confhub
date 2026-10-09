FROM registry.cn-hangzhou.aliyuncs.com/bodesi/node:24.12 AS builder

WORKDIR /app

# Use the domestic mirror for public packages and install pnpm directly.
RUN npm config set registry https://registry.npmmirror.com \
    && npm install --global pnpm@10.4.1 \
    && pnpm config set registry https://registry.npmmirror.com
COPY package.json pnpm-lock.yaml ./

RUN pnpm install --frozen-lockfile \

COPY . .

RUN npm run build:no-check

FROM registry.cn-hangzhou.aliyuncs.com/bodesi/nginx:1.27-alpine AS runtime

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /data/nginx/html/

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget --no-verbose --tries=1 --spider http://127.0.0.1/ || exit 1