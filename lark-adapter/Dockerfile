FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY main.go ./

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/lark-alertmanager-adapter .

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /out/lark-alertmanager-adapter /lark-alertmanager-adapter

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/lark-alertmanager-adapter"]
