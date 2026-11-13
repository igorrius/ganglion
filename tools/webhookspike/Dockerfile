# The webhook spike of task 6.6 (ADR-0078): one static binary, no volume.
# Deploy this folder alone: railway up tools/webhookspike --path-as-root -s <service>
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init webhookspike && CGO_ENABLED=0 go build -o /webhookspike .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /webhookspike /webhookspike
USER 65534
ENTRYPOINT ["/webhookspike"]
