# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
ARG CMD=api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /srv
COPY --from=build /out/app /srv/app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Static assets served by the API (Swagger UI + OpenAPI spec).
COPY --from=build /src/docs /srv/docs
USER nonroot:nonroot
ENTRYPOINT ["/srv/app"]
