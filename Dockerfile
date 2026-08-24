FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/meeting-room ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=build /out/meeting-room /usr/local/bin/meeting-room
VOLUME /data
ENV MEETING_ROOM_DATA_DIR=/data
EXPOSE 8090
ENTRYPOINT ["meeting-room"]
