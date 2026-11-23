FROM golang:1.26-alpine AS build
ARG CMD=ingestion
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /service ./cmd/${CMD}

# Image minimale, utilisateur non privilégié.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /service /service
ENTRYPOINT ["/service"]
