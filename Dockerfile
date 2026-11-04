FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /calculator ./cmd/calculator

FROM debian:bookworm-slim
RUN mkdir /data && chown 10001:10001 /data
COPY --from=build /calculator /usr/local/bin/calculator
USER 10001:10001
WORKDIR /data
ENV HTTP_PORT=8080 DATABASE_PATH=/data/calculator.db
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["calculator"]
