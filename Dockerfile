FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services ./services
ARG SERVICE
RUN CGO_ENABLED=0 go build -o /app ./services/${SERVICE}

FROM alpine:3.20
COPY --from=build /app /app
ENTRYPOINT ["/app"]
