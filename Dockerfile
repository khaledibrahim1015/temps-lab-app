FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${APP_VERSION}" -o /app .

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app"]
