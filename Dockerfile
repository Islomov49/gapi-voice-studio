# The Studio as one image: the built interface and the service.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS server
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /studio ./cmd/studio

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 studio && mkdir /data && chown studio /data
COPY --from=server /studio /usr/local/bin/studio
COPY --from=web /web/dist /srv/web
USER studio
ENV STUDIO_WEB_DIR=/srv/web STUDIO_LISTEN=0.0.0.0:8090 STUDIO_SECRET_FILE=/data/secret
VOLUME /data
EXPOSE 8090
ENTRYPOINT ["studio"]
