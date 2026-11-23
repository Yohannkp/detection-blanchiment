FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ingestion ./cmd/ingestion

# Image minimale, utilisateur non privilégié.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ingestion /ingestion
EXPOSE 8090
ENTRYPOINT ["/ingestion"]
