# The sandbox probe as a container, for Railway: M3d task 3.20.
# docker build -f tools/sandboxprobe/Dockerfile -t sandboxprobe .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /probe ./tools/sandboxprobe

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /probe /probe
ENTRYPOINT ["/probe", "-hold"]
