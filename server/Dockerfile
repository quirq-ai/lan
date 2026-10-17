FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lan-server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /lan-server /lan-server
USER nonroot
ENV LAN_STATE=/data/state.json LAN_LISTEN=:8443
EXPOSE 8443
ENTRYPOINT ["/lan-server"]
