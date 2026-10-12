# syntax=docker/dockerfile:1

FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/agora ./cmd/agora
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agora /usr/local/bin/agora
COPY --from=build --chown=nonroot:nonroot /out/data /data
# Published ports are reached as localhost; set AGORA_ALLOWED_HOSTS to the
# names clients use (or *) when exposing the server beyond this machine.
ENV AGORA_DB=/data/agora.db \
    AGORA_ADDR=:8787 \
    AGORA_ALLOWED_HOSTS=localhost,127.0.0.1,::1
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["agora"]
CMD ["serve"]
