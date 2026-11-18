FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/airband-hub ./cmd/airband-hub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/airband-hub /airband-hub
EXPOSE 8000
ENTRYPOINT ["/airband-hub"]
