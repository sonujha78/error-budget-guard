# Builds the two demo programs. Local use only: the containers run as root so the bind mounted
# result directory is writable.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/ebg-demoapp ./cmd/ebg-demoapp \
 && CGO_ENABLED=0 go build -trimpath -o /out/ebg-loadgen ./cmd/ebg-loadgen

FROM alpine:3.20
COPY --from=build /out/ /usr/local/bin/
