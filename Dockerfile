FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/airband-hub ./cmd/airband-hub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/airband-hub /airband-hub
EXPOSE 8000
ENTRYPOINT ["/airband-hub"]
