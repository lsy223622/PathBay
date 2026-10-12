FROM golang:1.27.2-alpine AS build
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -trimpath -ldflags='-s -w -linkmode external -extldflags "-static"' -o /out/pathbay ./cmd/pathbay

FROM scratch AS binary
COPY --from=build /out/pathbay /pathbay

FROM alpine:3.22 AS runtime
RUN apk add --no-cache tzdata ca-certificates && mkdir /data
COPY --from=build /out/pathbay /usr/local/bin/pathbay
WORKDIR /data
EXPOSE 7284
ENTRYPOINT ["pathbay"]
CMD ["--listen", "0.0.0.0:7284", "--data-dir", "/data"]

