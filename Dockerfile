FROM golang:1.26.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG PACKAGE
ARG BINARY
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/${BINARY} ${PACKAGE}

FROM debian:12.12-slim
ARG BINARY
COPY --from=build /out/${BINARY} /usr/local/bin/app
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/app"]
