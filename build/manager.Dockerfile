FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/fleet-manager ./cmd/fleet-manager \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/fleet-registration ./cmd/fleet-registration \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/fleet-simulator ./cmd/fleet-simulator \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/fleet-dhcp-sync ./cmd/fleet-dhcp-sync \
 && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/fleet-bmc-discovery ./cmd/fleet-bmc-discovery

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/fleet-manager /fleet-manager
COPY --from=build /out/fleet-registration /fleet-registration
COPY --from=build /out/fleet-simulator /fleet-simulator
COPY --from=build /out/fleet-dhcp-sync /fleet-dhcp-sync
COPY --from=build /out/fleet-bmc-discovery /fleet-bmc-discovery
ENTRYPOINT ["/fleet-manager"]
