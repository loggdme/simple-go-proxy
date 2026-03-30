FROM golang:alpine AS builder
WORKDIR /build

COPY go.mod ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o proxy .


FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /build/proxy /proxy
EXPOSE 8888
ENTRYPOINT ["/proxy"]
